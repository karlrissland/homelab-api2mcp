package mcpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"

	"github.com/karlrissland/homelab-api2mcp/internal/adminapi"
	"github.com/karlrissland/homelab-api2mcp/internal/discovery"
	"github.com/karlrissland/homelab-api2mcp/internal/keys"
	"github.com/karlrissland/homelab-api2mcp/internal/passthrough"
	"github.com/karlrissland/homelab-api2mcp/internal/pipeline"
	"github.com/karlrissland/homelab-api2mcp/internal/render"
	"github.com/karlrissland/homelab-api2mcp/internal/skillstools"
)

func TestRuntimeHandlerPassthroughTierFilteringAndRelay(t *testing.T) {
	t.Parallel()

	var publicCalls atomic.Int32
	var adminCalls atomic.Int32

	upstreamServer := mcp.NewServer(&mcp.Implementation{Name: "upstream", Version: "0.0.1"}, nil)
	mcp.AddTool(upstreamServer, &mcp.Tool{
		Name: "echo_public",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
			},
			"required": []string{"name"},
		},
	}, func(_ context.Context, _ *mcp.CallToolRequest, params struct {
		Name string `json:"name"`
	}) (*mcp.CallToolResult, any, error) {
		publicCalls.Add(1)
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: "hello " + params.Name},
			},
		}, nil, nil
	})
	mcp.AddTool(upstreamServer, &mcp.Tool{Name: "secret_admin"}, func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		adminCalls.Add(1)
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: "top secret"},
			},
		}, nil, nil
	})

	upstream := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return upstreamServer
	}, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
	}))
	defer upstream.Close()

	client := kubernetesfake.NewSimpleClientset()
	table, err := discovery.New(client)
	if err != nil {
		t.Fatalf("discovery.New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- table.Run(ctx)
	}()
	defer func() {
		cancel()
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("table.Run() error = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for discovery shutdown")
		}
	}()

	store := keys.NewStore()
	writer, err := keys.NewSecretWriter(client)
	if err != nil {
		t.Fatalf("keys.NewSecretWriter() error = %v", err)
	}
	admin, err := adminapi.New(client, table, store, writer)
	if err != nil {
		t.Fatalf("adminapi.New() error = %v", err)
	}
	skillGVR := schema.GroupVersionResource{Group: skillstools.Group, Version: skillstools.Version, Resource: skillstools.Resource}
	skillClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		skillGVR: skillstools.Kind + "List",
	}, &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": skillstools.Group + "/" + skillstools.Version,
			"kind":       skillstools.Kind,
			"metadata": map[string]any{
				"name":      "mcp2rest-usage",
				"namespace": "default",
			},
			"spec": map[string]any{
				"skillName":   "mcp2rest usage",
				"description": "How to use mcp2rest",
				"domain":      "mcp2rest",
				"type":        "parent",
				"confidence":  "high",
				"source":      "earned",
				"content":     "# mcp2rest",
			},
		},
	})
	adminKey, created, err := admin.EnsureBootstrapAdminKey(testWriter{t})
	if err != nil {
		t.Fatalf("EnsureBootstrapAdminKey() error = %v", err)
	}
	if !created || adminKey == "" {
		t.Fatalf("EnsureBootstrapAdminKey() = (%q, %t), want created bootstrap key", adminKey, created)
	}

	handler, err := NewRuntimeHandler(
		table,
		store,
		render.New(http.DefaultClient),
		passthrough.New(upstream.Client()),
		admin,
		skillClient,
	)
	if err != nil {
		t.Fatalf("NewRuntimeHandler() error = %v", err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	adminSession := connectHTTPClient(t, server.URL+managementPath, adminKey)
	defer func() { _ = adminSession.Close() }()

	registerResult, err := adminSession.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "register_app",
		Arguments: map[string]any{
			"namespace": "apps",
			"manifest": map[string]any{
				"name":           "relay-demo",
				"upstreamMCPURL": upstream.URL,
				"tools": []any{
					map[string]any{
						"name":             "public_echo",
						"description":      "Public passthrough tool",
						"tier":             "user",
						"type":             "passthrough",
						"upstreamToolName": "echo_public",
						"inputSchema": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"name": map[string]any{"type": "string"},
							},
						},
					},
					map[string]any{
						"name":             "admin_secret",
						"description":      "Admin-only passthrough tool",
						"tier":             "admin",
						"type":             "passthrough",
						"upstreamToolName": "secret_admin",
					},
				},
			},
			"authorizedAgents": []any{
				map[string]any{"agentInstance": "hermes-alice", "namespace": "agents"},
			},
		},
	})
	if err != nil {
		t.Fatalf("CallTool(register_app) error = %v", err)
	}
	if registerResult.IsError {
		t.Fatalf("register_app tool error: %s", joinedText(registerResult))
	}

	waitForDiscoveredApp(t, table, "relay-demo")

	rawKeys, err := writer.ReadSecretKeys(context.Background(), "hermes-alice", "agents")
	if err != nil {
		t.Fatalf("ReadSecretKeys() error = %v", err)
	}
	if len(rawKeys) != 1 {
		t.Fatalf("ReadSecretKeys() = %#v, want exactly one user key", rawKeys)
	}

	appSession := connectHTTPClient(t, server.URL+"/relay-demo/mcp", rawKeys[0])
	defer func() { _ = appSession.Close() }()

	tools, err := appSession.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	names := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	if got, want := strings.Join(names, ","), "get_skill,list_skills,public_echo"; got != want {
		t.Fatalf("ListTools() names = %q, want %q", got, want)
	}

	callResult, err := appSession.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "public_echo",
		Arguments: map[string]any{"name": "alice"},
	})
	if err != nil {
		t.Fatalf("CallTool(public_echo) error = %v", err)
	}
	if callResult.IsError {
		t.Fatalf("public_echo tool error: %s", joinedText(callResult))
	}
	if got := strings.TrimSpace(joinedText(callResult)); got != "hello alice" {
		t.Fatalf("CallTool(public_echo) text = %q, want %q", got, "hello alice")
	}
	if got := publicCalls.Load(); got != 1 {
		t.Fatalf("public upstream calls = %d, want 1", got)
	}

	forbiddenResult, err := appSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "admin_secret"})
	if err != nil {
		t.Fatalf("CallTool(admin_secret) protocol error = %v", err)
	}
	if !forbiddenResult.IsError {
		t.Fatal("CallTool(admin_secret) IsError = false, want true")
	}
	if got := joinedText(forbiddenResult); !strings.Contains(got, pipeline.ErrTierForbidden.Error()) {
		t.Fatalf("CallTool(admin_secret) text = %q, want substring %q", got, pipeline.ErrTierForbidden.Error())
	}
	if got := adminCalls.Load(); got != 0 {
		t.Fatalf("admin upstream calls = %d, want 0", got)
	}
}
