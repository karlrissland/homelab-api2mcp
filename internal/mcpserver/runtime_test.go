package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
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
	"github.com/karlrissland/homelab-api2mcp/internal/render"
	"github.com/karlrissland/homelab-api2mcp/internal/skillstools"
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
				"labels":    map[string]any{"skills.homelab.dev/topic": "overview"},
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

	handler, err := NewRuntimeHandler(table, store, render.New(upstream.Client()), passthrough.New(upstream.Client()), admin, skillClient)
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
	names := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	if got, want := strings.Join(names, ","), "get_skill,list_repos,list_skills"; got != want {
		t.Fatalf("ListTools() names = %q, want %q", got, want)
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

	listSkills, err := appSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_skills"})
	if err != nil {
		t.Fatalf("CallTool(list_skills) error = %v", err)
	}
	if listSkills.IsError {
		t.Fatalf("list_skills tool error: %s", joinedText(listSkills))
	}
	decodedSkills := decodeStructured[skillstools.ListSkillsResult](t, listSkills.StructuredContent)
	if len(decodedSkills.Skills) != 1 || decodedSkills.Skills[0].Name != "mcp2rest-usage" {
		t.Fatalf("list_skills structured result = %+v, want mcp2rest-usage summary", decodedSkills)
	}

	getSkill, err := appSession.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "get_skill",
		Arguments: map[string]any{"name": "mcp2rest-usage"},
	})
	if err != nil {
		t.Fatalf("CallTool(get_skill) error = %v", err)
	}
	if getSkill.IsError {
		t.Fatalf("get_skill tool error: %s", joinedText(getSkill))
	}
	decodedSkill := decodeStructured[skillstools.GetSkillResult](t, getSkill.StructuredContent)
	if decodedSkill.Skill.Spec.Content != "# mcp2rest" {
		t.Fatalf("get_skill structured result = %+v, want full skill content", decodedSkill)
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

func decodeStructured[T any](t *testing.T, value any) T {
	t.Helper()

	var out T
	bytes, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if err := json.Unmarshal(bytes, &out); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	return out
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
