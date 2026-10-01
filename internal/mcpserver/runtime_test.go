package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/karlrissland/homelab-api2mcp/internal/adminapi"
	"github.com/karlrissland/homelab-api2mcp/internal/discovery"
	"github.com/karlrissland/homelab-api2mcp/internal/keys"
	"github.com/karlrissland/homelab-api2mcp/internal/render"
)

func TestRuntimeHandlerRegisterAppAndRenderedCallRoundTrip(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos" {
			t.Fatalf("upstream path = %q, want /repos", r.URL.Path)
		}
		if got := r.URL.Query().Get("page"); got != "2" {
			t.Fatalf("upstream page query = %q, want 2", got)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode([]map[string]any{
			{"full_name": "octocat/hello-world"},
		}); err != nil {
			t.Fatalf("encode upstream response: %v", err)
		}
	}))
	defer upstream.Close()

	client := fake.NewSimpleClientset()
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
	adminKey, created, err := admin.EnsureBootstrapAdminKey(testWriter{t})
	if err != nil {
		t.Fatalf("EnsureBootstrapAdminKey() error = %v", err)
	}
	if !created || adminKey == "" {
		t.Fatalf("EnsureBootstrapAdminKey() = (%q, %t), want created bootstrap key", adminKey, created)
	}

	handler, err := NewRuntimeHandler(table, store, render.New(upstream.Client()), admin)
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
				"name":            "demo",
				"upstreamBaseURL": upstream.URL,
				"tools": []any{
					map[string]any{
						"name":        "list_repos",
						"description": "List repos",
						"tier":        "user",
						"type":        "rendered",
						"inputSchema": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"page": map[string]any{"type": "integer"},
							},
						},
						"requestTemplate":  `{"method":"GET","path":"/repos?page={{ args.page }}"}`,
						"responseTemplate": `{% for repo in response.body %}{{ repo.full_name }}{% endfor %}`,
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

	waitForDiscoveredApp(t, table, "demo")

	rawKeys, err := writer.ReadSecretKeys(context.Background(), "hermes-alice", "agents")
	if err != nil {
		t.Fatalf("ReadSecretKeys() error = %v", err)
	}
	if len(rawKeys) != 1 {
		t.Fatalf("ReadSecretKeys() = %#v, want exactly one user key", rawKeys)
	}

	appSession := connectHTTPClient(t, server.URL+"/demo/mcp", rawKeys[0])
	defer func() { _ = appSession.Close() }()

	tools, err := appSession.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != "list_repos" {
		t.Fatalf("ListTools() = %+v, want one list_repos tool", tools.Tools)
	}

	callResult, err := appSession.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_repos",
		Arguments: map[string]any{"page": 2},
	})
	if err != nil {
		t.Fatalf("CallTool(list_repos) error = %v", err)
	}
	if callResult.IsError {
		t.Fatalf("list_repos tool error: %s", joinedText(callResult))
	}
	if got := strings.TrimSpace(joinedText(callResult)); got != "octocat/hello-world" {
		t.Fatalf("CallTool(list_repos) text = %q, want %q", got, "octocat/hello-world")
	}
}

type authTransport struct {
	token string
	base  http.RoundTripper
}

func (t authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	cloned.Header = req.Header.Clone()
	cloned.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(cloned)
}

type testWriter struct {
	t *testing.T
}

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

func connectHTTPClient(t *testing.T, endpoint, token string) *mcp.ClientSession {
	t.Helper()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: endpoint,
		HTTPClient: &http.Client{
			Transport: authTransport{token: token, base: http.DefaultTransport},
		},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("client.Connect(%s) error = %v", endpoint, err)
	}
	return session
}

func joinedText(result *mcp.CallToolResult) string {
	parts := make([]string, 0, len(result.Content))
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func waitForDiscoveredApp(t *testing.T, table *discovery.Table, appName string) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := table.Get(appName); ok {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("app %q did not appear in discovery table", appName)
}
