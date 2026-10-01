package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	"github.com/karlrissland/homelab-api2mcp/internal/manifest"
	"github.com/karlrissland/homelab-api2mcp/internal/passthrough"
	"github.com/karlrissland/homelab-api2mcp/internal/render"
	"github.com/karlrissland/homelab-api2mcp/internal/skillstools"
)

func TestRuntimeHandlerCallerUsernameImpersonationRoundTrip(t *testing.T) {
	t.Parallel()

	type capturedRequest struct {
		impersonated string
		page         string
	}

	var (
		mu       sync.Mutex
		captured []capturedRequest
	)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		captured = append(captured, capturedRequest{
			impersonated: r.Header.Get("X-Impersonate-User"),
			page:         r.URL.Query().Get("page"),
		})
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"impersonated": r.Header.Get("X-Impersonate-User"),
		}); err != nil {
			t.Fatalf("encode upstream response: %v", err)
		}
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
	creds := mustCreds(t, client)
	admin, err := adminapi.New(client, table, store, writer, creds)
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

	metrics, logger, metricsHandler := testRuntimeObservability(t)
	handler, err := NewRuntimeHandler(table, store, render.New(upstream.Client()), passthrough.New(upstream.Client()), creds, admin, skillClient, metrics, logger, metricsHandler)
	if err != nil {
		t.Fatalf("NewRuntimeHandler() error = %v", err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	fixtureApp := loadRenderFixture(t, "mock-multi-user")
	fixtureApp.UpstreamBaseURL = upstream.URL

	adminSession := connectHTTPClient(t, server.URL+managementPath, adminKey)
	defer func() { _ = adminSession.Close() }()

	registerResult, err := adminSession.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "register_app",
		Arguments: map[string]any{
			"namespace": "apps",
			"manifest":  appToMap(t, fixtureApp),
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

	waitForDiscoveredApp(t, table, fixtureApp.Name)

	rawKeys, err := writer.ReadSecretKeys(context.Background(), "hermes-alice", "agents")
	if err != nil {
		t.Fatalf("ReadSecretKeys() error = %v", err)
	}
	if len(rawKeys) != 1 {
		t.Fatalf("ReadSecretKeys() = %#v, want exactly one user key", rawKeys)
	}

	appSession := connectHTTPClient(t, server.URL+"/"+fixtureApp.Name+"/mcp", rawKeys[0])
	defer func() { _ = appSession.Close() }()

	callAlice, err := appSession.CallTool(context.Background(), &mcp.CallToolParams{
		Name: fixtureApp.Tools[0].Name,
		Arguments: map[string]any{
			"page":                 2,
			callerUsernameArgument: "alice",
		},
	})
	if err != nil {
		t.Fatalf("CallTool(alice) error = %v", err)
	}
	if callAlice.IsError {
		t.Fatalf("CallTool(alice) tool error: %s", joinedText(callAlice))
	}

	callBob, err := appSession.CallTool(context.Background(), &mcp.CallToolParams{
		Name: fixtureApp.Tools[0].Name,
		Arguments: map[string]any{
			"page":                 2,
			callerUsernameArgument: "bob",
		},
	})
	if err != nil {
		t.Fatalf("CallTool(bob) error = %v", err)
	}
	if callBob.IsError {
		t.Fatalf("CallTool(bob) tool error: %s", joinedText(callBob))
	}

	if got := strings.TrimSpace(joinedText(callAlice)); got != "alice" {
		t.Fatalf("CallTool(alice) text = %q, want %q", got, "alice")
	}
	if got := strings.TrimSpace(joinedText(callBob)); got != "bob" {
		t.Fatalf("CallTool(bob) text = %q, want %q", got, "bob")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(captured) != 2 {
		t.Fatalf("captured upstream requests = %d, want 2", len(captured))
	}
	if captured[0].impersonated != "alice" || captured[1].impersonated != "bob" {
		t.Fatalf("captured impersonation headers = %+v, want alice then bob", captured)
	}
	if captured[0].page != "2" || captured[1].page != "2" {
		t.Fatalf("captured page query = %+v, want page=2 on both calls", captured)
	}
}

func loadRenderFixture(t *testing.T, name string) manifest.App {
	t.Helper()

	dir := filepath.Join("..", "render", "testdata", name)

	manifestBytes, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest fixture: %v", err)
	}

	var app manifest.App
	if err := json.Unmarshal(manifestBytes, &app); err != nil {
		t.Fatalf("unmarshal manifest fixture: %v", err)
	}
	if len(app.Tools) != 1 {
		t.Fatalf("fixture tools = %d, want 1", len(app.Tools))
	}

	requestTemplate, err := os.ReadFile(filepath.Join(dir, "request.liquid"))
	if err != nil {
		t.Fatalf("read request fixture: %v", err)
	}
	responseTemplate, err := os.ReadFile(filepath.Join(dir, "response.liquid"))
	if err != nil {
		t.Fatalf("read response fixture: %v", err)
	}

	app.Tools[0].RequestTemplate = string(requestTemplate)
	app.Tools[0].ResponseTemplate = string(responseTemplate)
	if err := app.Tools[0].Validate(); err != nil {
		t.Fatalf("validate fixture tool: %v", err)
	}

	return app
}

func appToMap(t *testing.T, app manifest.App) map[string]any {
	t.Helper()

	bytes, err := json.Marshal(app)
	if err != nil {
		t.Fatalf("json.Marshal(app) error = %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal(bytes, &out); err != nil {
		t.Fatalf("json.Unmarshal(app) error = %v", err)
	}
	return out
}
