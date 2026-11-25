package mcpserver

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"

	"github.com/karlrissland/homelab-api2mcp/internal/adminapi"
	"github.com/karlrissland/homelab-api2mcp/internal/discovery"
	"github.com/karlrissland/homelab-api2mcp/internal/keys"
	"github.com/karlrissland/homelab-api2mcp/internal/manifest"
	"github.com/karlrissland/homelab-api2mcp/internal/passthrough"
	"github.com/karlrissland/homelab-api2mcp/internal/render"
)

func TestHumanCredentialsAuthenticateActualDeclaredReadsAfterRestart(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		if req.Header.Get("X-Agent") != "" {
			t.Error("human was relabeled as an agent")
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, `{"temperature":21}`); err != nil {
			t.Error(err)
		}
	}))
	defer upstream.Close()
	client := kubernetesfake.NewSimpleClientset()
	table, err := discovery.New(client)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- table.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	writer, err := keys.NewSecretWriter(client)
	if err != nil {
		t.Fatal(err)
	}
	creds := mustCreds(t, client)
	start := func() (*adminapi.API, *httptest.Server, *keys.Store) {
		t.Helper()
		store := keys.NewStore()
		agents := newAgentKeys(t, writer, store)
		humans, err := keys.NewHumanKeys(writer, "mcp2rest", store)
		if err != nil {
			t.Fatal(err)
		}
		if err := humans.Restore(ctx); err != nil {
			t.Fatal(err)
		}
		admin, err := adminapi.New(client, table, store, agents, creds)
		if err != nil {
			t.Fatal(err)
		}
		if err := admin.SetHumanKeys(humans); err != nil {
			t.Fatal(err)
		}
		metrics, logger, metricsHandler := testRuntimeObservability(t)
		skills := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
		handler, err := NewRuntimeHandler(table, store, render.New(upstream.Client()), passthrough.New(upstream.Client()), passthrough.NewProxy(upstream.Client().Transport, logger), creds, admin, skills, metrics, logger, metricsHandler)
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(handler)
		t.Cleanup(server.Close)
		return admin, server, store
	}
	api, first, store := start()
	adminKey := bootstrapAdminKey(t, client, writer, store)
	if _, err := api.RegisterApp(ctx, adminapi.RegisterAppParams{
		Namespace: "apps",
		Manifest: manifest.App{
			Name: "widget", UpstreamBaseURL: upstream.URL,
			Tools: []manifest.Tool{
				{Name: "read_value", Tier: manifest.TierUser, Type: manifest.ToolTypeRendered,
					RequestTemplate:  `{"method":"GET","path":"/value","headers":{"X-Agent":"{{ caller.agentInstance }}"}}`,
					ResponseSchema:   map[string]any{"type": "object", "required": []any{"temperature"}},
					ResponseTemplate: `{"temperature":{{ response.body.temperature }}}`},
				{Name: "read_admin", Tier: manifest.TierAdmin, Type: manifest.ToolTypeRendered,
					RequestTemplate: `{"method":"GET","path":"/admin"}`, ResponseTemplate: `{{ response.rawBody }}`},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	waitForDiscoveredApp(t, table, "widget")
	put, err := api.PutHuman(ctx, adminKey, adminapi.PutHumanParams{
		APIVersion: 1, HomelabUserID: "canonical-human", Source: "hlctl", SourceRevision: "snapshot-1",
		State: keys.HumanActive, Admin: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	params := adminapi.HumanKeyParams{APIVersion: 1, HomelabUserID: "canonical-human", Tier: manifest.TierUser, ExpectedRevision: put.Human.Revision}
	user, err := api.CreateHumanKey(ctx, adminKey, params)
	if err != nil {
		t.Fatal(err)
	}
	params.Tier = manifest.TierAdmin
	admin, err := api.CreateHumanKey(ctx, adminKey, params)
	if err != nil {
		t.Fatal(err)
	}
	first.Close()
	api, server, store := start()
	if _, err := writer.EnsureBootstrap(ctx, "mcp2rest", store); err != nil {
		t.Fatal(err)
	}
	userSession := connectHTTPClient(t, server.URL+"/widget/mcp", user.Credential.Key)
	defer func() {
		if err := userSession.Close(); err != nil {
			t.Error(err)
		}
	}()
	list, err := userSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range list.Tools {
		if tool.Name == "read_admin" {
			t.Fatal("user human discovered an admin-only declared tool")
		}
	}
	read, err := userSession.CallTool(ctx, &mcp.CallToolParams{Name: "read_value"})
	if err != nil || read.IsError || joinedText(read) != `{"temperature":21}` {
		t.Fatal("persisted human key did not perform a useful authenticated declared read")
	}
	before := calls.Load()
	denied, err := userSession.CallTool(ctx, &mcp.CallToolParams{Name: "read_admin"})
	if err != nil || !denied.IsError || calls.Load() != before {
		t.Fatal("user human bypassed the existing tool-tier boundary")
	}
	adminSession := connectHTTPClient(t, server.URL+"/widget/mcp", admin.Credential.Key)
	defer func() {
		if err := adminSession.Close(); err != nil {
			t.Error(err)
		}
	}()
	read, err = adminSession.CallTool(ctx, &mcp.CallToolParams{Name: "read_admin"})
	if err != nil || read.IsError || calls.Load() != before+1 {
		t.Fatal("eligible human admin could not call the existing admin-tier tool")
	}
	if _, err := api.PutHuman(ctx, adminKey, adminapi.PutHumanParams{
		APIVersion: 1, HomelabUserID: "canonical-human", Source: "hlctl", SourceRevision: "snapshot-2",
		State: keys.HumanDisabled, ExpectedRevision: put.Human.Revision,
	}); err != nil {
		t.Fatal(err)
	}
	before = calls.Load()
	denied, err = userSession.CallTool(ctx, &mcp.CallToolParams{Name: "read_value"})
	if err != nil || !denied.IsError || calls.Load() != before {
		t.Fatal("disabled human reached an upstream through a previously connected session")
	}
}
