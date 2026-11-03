package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	"github.com/karlrissland/homelab-api2mcp/internal/pipeline"
	"github.com/karlrissland/homelab-api2mcp/internal/render"
	"github.com/karlrissland/homelab-api2mcp/internal/skillstools"
)

func TestRuntimeHandlerRegisterAppAndRenderedCallRoundTrip(t *testing.T) {
	t.Parallel()

	const description = "  Producer \"demo\" text: caf\u00e9.\r\n{{ caller.username }} {% raw %}\tDo not normalize.  "
	const adminDescription = "Inspect demo repositories; gateway admin tier required, not an upstream role."
	var upstreamRequests atomic.Int32

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamRequests.Add(1)
		if r.URL.Path != "/repos" {
			t.Fatalf("upstream path = %q, want /repos", r.URL.Path)
		}
		if got := r.URL.Query().Get("page"); got != "2" && got != "3" {
			t.Fatalf("upstream page query = %q, want 2 or 3", got)
		}

		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "3" {
			w.WriteHeader(http.StatusForbidden)
		}
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
	creds := mustCreds(t, client)
	admin, err := adminapi.New(client, table, store, newAgentKeys(t, writer, store), creds)
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
	}, &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": skillstools.Group + "/" + skillstools.Version,
			"kind":       skillstools.Kind,
			"metadata": map[string]any{
				"name": "demo-guidance", "namespace": "apps",
				"labels": map[string]any{"skills.homelab.dev/app": "demo"},
			},
			"spec": map[string]any{
				"skillName": "Demo guidance", "description": "Demo documentation", "domain": "demo",
				"type": "parent", "confidence": "high", "source": "earned", "content": "# Demo",
			},
		},
	}, &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": skillstools.Group + "/" + skillstools.Version,
			"kind":       skillstools.Kind,
			"metadata": map[string]any{
				"name": "other-guidance", "namespace": "other",
				"labels": map[string]any{"skills.homelab.dev/app": "other"},
			},
			"spec": map[string]any{
				"skillName": "Other guidance", "description": "Other documentation", "domain": "other",
				"type": "parent", "confidence": "high", "source": "earned", "content": "# Other",
			},
		},
	})
	adminKey := bootstrapAdminKey(t, client, writer, store)

	metrics, logger, metricsHandler := testRuntimeObservability(t)
	handler, err := NewRuntimeHandler(table, store, render.New(upstream.Client()), passthrough.New(upstream.Client()), passthrough.NewProxy(upstream.Client().Transport, logger), creds, admin, skillClient, metrics, logger, metricsHandler)
	if err != nil {
		t.Fatalf("NewRuntimeHandler() error = %v", err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	for _, tc := range []struct {
		name  string
		token string
	}{
		{name: "missing bootstrap credential"},
		{name: "unknown bootstrap credential", token: "unknown-key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := connectHTTPClient(t, server.URL+managementPath, tc.token)
			defer func() { _ = session.Close() }()
			assertManagementDescriptions(t, session)
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_apps"})
			if err != nil {
				t.Fatalf("list_apps transport error = %v", err)
			}
			if !result.IsError {
				t.Fatal("management call succeeded without the persisted bootstrap credential")
			}
		})
	}

	adminSession := connectHTTPClient(t, server.URL+managementPath, adminKey)
	defer func() { _ = adminSession.Close() }()

	if got := adminSession.InitializeResult().Instructions; !strings.Contains(got, "MANAGEMENT endpoint") {
		t.Fatalf("management session Instructions = %q, want it to identify the management endpoint", got)
	}
	assertManagementDescriptions(t, adminSession)
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
						"description": description,
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
					map[string]any{
						"name": "admin_list_repos", "description": adminDescription,
						"tier": "admin", "type": "rendered",
						"requestTemplate": `{"method":"GET","path":"/repos?page=2"}`, "responseTemplate": `admin`,
					},
					map[string]any{
						"name": "legacy_empty", "description": "",
						"tier": "user", "type": "rendered",
						"requestTemplate": `{"method":"GET","path":"/repos?page=2"}`, "responseTemplate": `legacy`,
					},
					map[string]any{
						"name": "legacy_omitted",
						"tier": "user", "type": "rendered",
						"requestTemplate": `{"method":"GET","path":"/repos?page=2"}`, "responseTemplate": `legacy`,
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

	agentKey, err := admin.GetKey(context.Background(), adminapi.GetKeyParams{AgentInstance: "hermes-alice", Namespace: "agents", Tier: "user"})
	if err != nil {
		t.Fatalf("GetKey() error = %v", err)
	}
	assertDescriptionFlow(t, client, adminSession, server.URL, "demo", agentKey.Key, adminKey, []manifest.Tool{
		{Name: "list_repos", Description: description, Tier: manifest.TierUser},
		{Name: "admin_list_repos", Description: adminDescription, Tier: manifest.TierAdmin},
		{Name: "legacy_empty", Tier: manifest.TierUser},
		{Name: "legacy_omitted", Tier: manifest.TierUser},
	})
	userManagement := connectHTTPClient(t, server.URL+managementPath, agentKey.Key)
	defer func() { _ = userManagement.Close() }()
	assertManagementDescriptions(t, userManagement)
	allSkills, err := userManagement.CallTool(ctx, &mcp.CallToolParams{Name: "list_skills"})
	if err != nil || allSkills.IsError {
		t.Fatalf("user management Skill read: result=%+v, error=%v", allSkills, err)
	}
	if got := decodeStructured[skillstools.ListSkillsResult](t, allSkills.StructuredContent).Skills; len(got) != 3 {
		t.Fatalf("cluster-wide Skill read returned %+v, want all three app/global documents", got)
	}
	for _, name := range []string{"list_apps", "list_keys", "create_skill"} {
		params := &mcp.CallToolParams{Name: name}
		if name == "create_skill" {
			params.Arguments = map[string]any{
				"name": "forbidden", "namespace": "apps",
				"spec": map[string]any{
					"skillName": "Forbidden", "description": "Test", "domain": "test",
					"type": "parent", "confidence": "high", "source": "earned", "content": "Test",
				},
			}
		}
		result, err := userManagement.CallTool(ctx, params)
		if err != nil || !result.IsError || !strings.Contains(joinedText(result), "admin tier is required") {
			t.Fatalf("user management call %s: result=%+v, error=%v, want tool error", name, result, err)
		}
	}

	appSession := connectHTTPClient(t, server.URL+"/demo/mcp", agentKey.Key)
	defer func() { _ = appSession.Close() }()

	if got := appSession.InitializeResult().Instructions; !strings.Contains(got, `"demo"`) {
		t.Fatalf("app session Instructions = %q, want it to name the connected app", got)
	}

	forbiddenResult, err := appSession.CallTool(ctx, &mcp.CallToolParams{Name: "admin_list_repos"})
	if err != nil || !forbiddenResult.IsError || !strings.Contains(joinedText(forbiddenResult), pipeline.ErrTierForbidden.Error()) {
		t.Fatalf("user admin-tool call: result=%+v, error=%v, want tier rejection", forbiddenResult, err)
	}
	if got := upstreamRequests.Load(); got != 0 {
		t.Fatalf("registration, discovery, and forbidden call made %d REST requests, want none", got)
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

	failedResult, err := appSession.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "list_repos", Arguments: map[string]any{"page": 3},
	})
	if err != nil || !failedResult.IsError || strings.TrimSpace(joinedText(failedResult)) != "octocat/hello-world" {
		t.Fatalf("REST rejection did not retain mapped content and isError: result=%+v, error=%v", failedResult, err)
	}

	listSkills, err := appSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_demo_skills"})
	if err != nil {
		t.Fatalf("CallTool(list_demo_skills) error = %v", err)
	}
	if listSkills.IsError {
		t.Fatalf("list_demo_skills tool error: %s", joinedText(listSkills))
	}
	decodedSkills := decodeStructured[skillstools.ListSkillsResult](t, listSkills.StructuredContent)
	if len(decodedSkills.Skills) != 2 || decodedSkills.Skills[0].Name != "demo-guidance" || decodedSkills.Skills[1].Name != "mcp2rest-usage" {
		t.Fatalf("list_demo_skills structured result = %+v, want app and global summaries across namespaces", decodedSkills)
	}
	otherSkill, err := appSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "get_demo_skill", Arguments: map[string]any{"name": "other-guidance", "namespace": "other"},
	})
	if err != nil || !otherSkill.IsError {
		t.Fatalf("other app's Skill read: result=%+v, error=%v, want tool error", otherSkill, err)
	}

	getSkill, err := appSession.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "get_demo_skill",
		Arguments: map[string]any{"name": "mcp2rest-usage"},
	})
	if err != nil {
		t.Fatalf("CallTool(get_demo_skill) error = %v", err)
	}
	if getSkill.IsError {
		t.Fatalf("get_demo_skill tool error: %s", joinedText(getSkill))
	}
	decodedSkill := decodeStructured[skillstools.GetSkillResult](t, getSkill.StructuredContent)
	if decodedSkill.Skill.Spec.Content != "# mcp2rest" {
		t.Fatalf("get_skill structured result = %+v, want full skill content", decodedSkill)
	}

	restartedStore := keys.NewStore()
	if created, err := writer.EnsureBootstrap(ctx, "mcp2rest", restartedStore); err != nil || created {
		t.Fatalf("restore bootstrap credential: created = %t, error = %v", created, err)
	}
	restartedAdmin, err := adminapi.New(client, table, restartedStore, newAgentKeys(t, writer, restartedStore), creds)
	if err != nil {
		t.Fatal(err)
	}
	restartedHandler, err := NewRuntimeHandler(table, restartedStore, render.New(upstream.Client()), passthrough.New(upstream.Client()), passthrough.NewProxy(upstream.Client().Transport, logger), creds, restartedAdmin, skillClient, metrics, logger, metricsHandler)
	if err != nil {
		t.Fatal(err)
	}
	restartedServer := httptest.NewServer(restartedHandler)
	defer restartedServer.Close()
	restartedManagement := connectHTTPClient(t, restartedServer.URL+managementPath, adminKey)
	defer func() { _ = restartedManagement.Close() }()
	fetched, err := restartedManagement.CallTool(ctx, &mcp.CallToolParams{
		Name: "get_key", Arguments: map[string]any{"agentInstance": "hermes-alice", "namespace": "agents", "tier": "user"},
	})
	if err != nil || fetched.IsError {
		t.Fatalf("get_key after restart: error = %v", err)
	}
	if decodeStructured[adminapi.GetKeyResult](t, fetched.StructuredContent).Key != agentKey.Key {
		t.Fatal("management key retrieval changed the agent credential after restart")
	}
	restartedSession := connectHTTPClient(t, restartedServer.URL+"/demo/mcp", agentKey.Key)
	defer func() { _ = restartedSession.Close() }()
	afterRestart, err := restartedSession.CallTool(ctx, &mcp.CallToolParams{Name: "list_repos", Arguments: map[string]any{"page": 2}})
	if err != nil || afterRestart.IsError || strings.TrimSpace(joinedText(afterRestart)) != "octocat/hello-world" {
		t.Fatalf("original agent key failed after restart: error = %v", err)
	}
}

func newAgentKeys(t *testing.T, writer *keys.SecretWriter, store *keys.Store) *keys.AgentKeys {
	t.Helper()
	agents, err := keys.NewAgentKeys(writer, "mcp2rest", store)
	if err != nil {
		t.Fatal(err)
	}
	if err := agents.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	return agents
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

func bootstrapAdminKey(t *testing.T, client *kubernetesfake.Clientset, writer *keys.SecretWriter, store *keys.Store) string {
	t.Helper()

	// Discard the first process's verification state to exercise authenticated
	// management calls using only the credential restored from Kubernetes.
	created, err := writer.EnsureBootstrap(context.Background(), "mcp2rest", keys.NewStore())
	if err != nil || !created {
		t.Fatalf("EnsureBootstrap(first startup) created = %t, error = %v", created, err)
	}
	created, err = writer.EnsureBootstrap(context.Background(), "mcp2rest", store)
	if err != nil || created {
		t.Fatalf("EnsureBootstrap(restart) created = %t, error = %v", created, err)
	}
	secret, err := client.CoreV1().Secrets("mcp2rest").Get(context.Background(), keys.BootstrapSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get(bootstrap Secret) error = %v", err)
	}
	return string(secret.Data[keys.BootstrapSecretDataKey])
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

func testRuntimeObservability(t *testing.T) (*pipeline.Metrics, *slog.Logger, http.Handler) {
	t.Helper()

	registry := prometheus.NewRegistry()
	metrics, err := pipeline.NewMetrics(registry)
	if err != nil {
		t.Fatalf("pipeline.NewMetrics() error = %v", err)
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	return metrics, logger, promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
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

func assertDescriptionFlow(t *testing.T, client *kubernetesfake.Clientset, management *mcp.ClientSession, gatewayURL, appName, userKey, adminKey string, want []manifest.Tool) {
	t.Helper()
	ctx := context.Background()
	checkManifest := func(t *testing.T, app manifest.App) {
		t.Helper()
		if app.Name != appName || len(app.Tools) != len(want) {
			t.Fatalf("manifest %q has %d tools, want %q with %d", app.Name, len(app.Tools), appName, len(want))
		}
		for _, expected := range want {
			tool, ok := app.FindTool(expected.Name)
			if !ok || tool.Description != expected.Description || tool.Tier != expected.Tier {
				t.Fatalf("manifest tool %q: description=%q, tier=%q, want description=%q, tier=%q", expected.Name, tool.Description, tool.Tier, expected.Description, expected.Tier)
			}
		}
	}
	cm, err := client.CoreV1().ConfigMaps("apps").Get(ctx, appName+"-mcp2rest", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := discovery.ParseConfigMap(cm)
	if err != nil {
		t.Fatal(err)
	}
	checkManifest(t, *persisted)
	for _, name := range []string{"get_manifest", "list_apps"} {
		params := &mcp.CallToolParams{Name: name}
		if name == "get_manifest" {
			params.Arguments = map[string]any{"appName": appName}
		}
		result, err := management.CallTool(ctx, params)
		if err != nil || result.IsError {
			t.Fatalf("%s: result=%+v, error=%v", name, result, err)
		}
		if name == "get_manifest" {
			checkManifest(t, decodeStructured[manifest.App](t, result.StructuredContent))
		} else {
			apps := decodeStructured[adminapi.ListAppsResult](t, result.StructuredContent).Apps
			if len(apps) != 1 {
				t.Fatalf("list_apps returned %d apps, want 1", len(apps))
			}
			checkManifest(t, *apps[0])
		}
	}
	for _, caller := range []struct {
		name, key string
		tier      manifest.Tier
	}{
		{name: "missing"},
		{name: "invalid", key: "unknown-key"},
		{name: "user", key: userKey, tier: manifest.TierUser},
		{name: "admin", key: adminKey, tier: manifest.TierAdmin},
	} {
		t.Run("description visibility "+caller.name, func(t *testing.T) {
			session := connectHTTPClient(t, gatewayURL+"/"+appName+"/mcp", caller.key)
			defer func() { _ = session.Close() }()
			listed, err := session.ListTools(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			slug := skillstools.ToolNameSlug(appName)
			listName, getName := "list_"+slug+"_skills", "get_"+slug+"_skill"
			expected := map[string]string{
				listName: "",
				getName:  "",
			}
			for _, tool := range want {
				if caller.tier.Valid() && caller.tier.Satisfies(tool.Tier) {
					expected[tool.Name] = tool.Description
				}
			}
			if len(listed.Tools) != len(expected) {
				t.Fatalf("tools/list has %d tools, want %d", len(listed.Tools), len(expected))
			}
			for _, tool := range listed.Tools {
				description, ok := expected[tool.Name]
				if !ok {
					t.Fatalf("unexpected tools/list entry %q", tool.Name)
				}
				if tool.Name == listName || tool.Name == getName {
					for _, fragment := range []string{
						`app "` + appName + `"`, "global Skill", "without an app label",
						"across namespaces", "user- or admin-tier gateway key",
						"not perform app actions", "Other apps' labeled Skills are excluded",
					} {
						if !strings.Contains(tool.Description, fragment) {
							t.Errorf("%s description missing %q: %q", tool.Name, fragment, tool.Description)
						}
					}
					continue
				}
				if tool.Description != description {
					t.Errorf("tools/list %q description=%q, want exact producer text %q", tool.Name, tool.Description, description)
				}
				registered, _ := persisted.FindTool(tool.Name)
				if got, want := decodeStructured[map[string]any](t, tool.InputSchema), schemaOrEmptyObject(registered.InputSchema); !reflect.DeepEqual(got, want) {
					t.Errorf("tools/list %q input schema=%v, want %v", tool.Name, got, want)
				}
				if tool.OutputSchema != nil {
					t.Errorf("tools/list %q unexpectedly advertises outputSchema", tool.Name)
				}
			}
		})
	}
}

func assertManagementDescriptions(t *testing.T, session *mcp.ClientSession) {
	t.Helper()
	want := map[string][]string{
		"register_app":   {"mcp2rest gateway", "not an app access allowlist", "not to call its API or connect a harness"},
		"deregister_app": {"mcp2rest gateway", "does not uninstall", "revoke existing gateway keys"},
		"list_apps":      {"mcp2rest gateway", "does not call app APIs, connect a harness, or return an API key", "Native registrations", "mcpInternalUrl", "mcpEndpointPath"},
		"get_manifest":   {"mcp2rest gateway", "does not change registration", "Native registrations", "mcpInternalUrl", "mcpEndpointPath"},
		"create_key":     {"mcp2rest gateway", "not app-scoped or upstream API credentials", "deliver"},
		"list_keys":      {"mcp2rest gateway", "does not return raw keys"},
		"get_key":        {"mcp2rest gateway", "protect the returned secret", "does not create or rotate"},
		"revoke_key":     {"mcp2rest gateway", "supplied agent namespace", "does not revoke server-owned upstream"},
		"rotate_key":     {"mcp2rest gateway", "manual rotation", "does not rotate server-owned upstream"},
		"reload_cache":   {"mcp2rest gateway", "does not rotate credentials or return their values"},
		"list_skills":    {"connected Kubernetes cluster", "all namespaces", "not perform app actions", "every app's Skills and global Skills", "get_skill"},
		"get_skill":      {"connected Kubernetes cluster", "any namespace", "not perform app actions", "metadata.name, not skillName", "list_skills"},
		"create_skill":   {"connected Kubernetes cluster", "supplied namespace", "does not register an app"},
		"update_skill":   {"connected Kubernetes cluster", "labels and entire spec", "not a patch"},
		"delete_skill":   {"connected Kubernetes cluster", "resource name and namespace", "does not uninstall an app"},
	}
	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != len(want) {
		t.Fatalf("management tools/list has %d tools, want %d", len(listed.Tools), len(want))
	}
	for _, tool := range listed.Tools {
		fragments, ok := want[tool.Name]
		if !ok {
			t.Fatalf("unexpected management tool %q", tool.Name)
		}
		requirement := "Requires an admin-tier gateway key."
		if tool.Name == "list_skills" || tool.Name == "get_skill" {
			requirement = "Requires a valid user- or admin-tier gateway key."
		}
		for _, fragment := range append(fragments, requirement) {
			if !strings.Contains(tool.Description, fragment) {
				t.Errorf("%s description missing %q: %q", tool.Name, fragment, tool.Description)
			}
		}
	}
}
