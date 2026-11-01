package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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

func TestRuntimeResponseSchemaContract(t *testing.T) {
	type fixture struct {
		body   string
		status int
	}
	var current atomic.Value
	current.Store(fixture{body: `{}`, status: 200})
	var requests atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet || (r.URL.Path != "/synthetic/items" && r.URL.Path != "/synthetic/results") {
			t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-Agent") != "synthetic-agent" || r.Header.Get("X-Tier") != "user" {
			t.Error("caller identity changed")
		}
		w.Header().Set("X-Secret", "synthetic-header-marker")
		value := current.Load().(fixture)
		w.WriteHeader(value.status)
		if _, err := io.WriteString(w, value.body); err != nil {
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
	store := keys.NewStore()
	writer, err := keys.NewSecretWriter(client)
	if err != nil {
		t.Fatal(err)
	}
	creds := mustCreds(t, client)
	admin, err := adminapi.New(client, table, store, newAgentKeys(t, writer, store), creds)
	if err != nil {
		t.Fatal(err)
	}
	adminKey := bootstrapAdminKey(t, client, writer, store)
	gvr := schema.GroupVersionResource{Group: skillstools.Group, Version: skillstools.Version, Resource: skillstools.Resource}
	skills := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: skillstools.Kind + "List"})
	metrics, _, metricsHandler := testRuntimeObservability(t)
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	handler, err := NewRuntimeHandler(table, store, render.New(upstream.Client()), passthrough.New(upstream.Client()), passthrough.NewProxy(upstream.Client().Transport, logger), creds, admin, skills, metrics, logger, metricsHandler)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	management := connectHTTPClient(t, server.URL+managementPath, adminKey)
	defer func() { _ = management.Close() }()

	tools := []any{}
	for _, key := range []string{"items", "results", "legacy", "admin_only"} {
		collection := key
		if key == "legacy" || key == "admin_only" {
			collection = "items"
		}
		tool := map[string]any{
			"name": key, "tier": "user", "type": "rendered",
			"requestTemplate":  `{"method":"GET","path":"/synthetic/` + collection + `","headers":{"X-Agent":"{{ caller.agentInstance }}","X-Tier":"{{ caller.tier }}","Authorization":"synthetic-credential-marker"}}`,
			"responseTemplate": `{% for item in response.body.` + collection + ` %}{{ item.name }}{% endfor %}`,
		}
		if key != "legacy" {
			tool["responseSchema"] = map[string]any{
				"type": "object", "required": []any{collection},
				"properties": map[string]any{collection: map[string]any{"type": "array"}},
			}
		}
		if key == "admin_only" {
			tool["tier"] = "admin"
		}
		tools = append(tools, tool)
	}
	registration, err := management.CallTool(ctx, &mcp.CallToolParams{
		Name: "register_app",
		Arguments: map[string]any{
			"namespace":        "apps",
			"manifest":         map[string]any{"name": "synthetic", "upstreamBaseURL": upstream.URL, "tools": tools},
			"authorizedAgents": []any{map[string]any{"agentInstance": "synthetic-agent", "namespace": "agents"}},
		},
	})
	if err != nil || registration.IsError {
		t.Fatalf("register_app failed: error=%v", err)
	}
	waitForDiscoveredApp(t, table, "synthetic")
	for _, name := range []string{"get_manifest", "list_apps"} {
		args := map[string]any{}
		if name == "get_manifest" {
			args["appName"] = "synthetic"
		}
		result, err := management.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || result.IsError {
			t.Fatalf("%s failed: error=%v", name, err)
		}
		var app manifest.App
		if name == "get_manifest" {
			app = decodeStructured[manifest.App](t, result.StructuredContent)
		} else {
			app = *decodeStructured[adminapi.ListAppsResult](t, result.StructuredContent).Apps[0]
		}
		if app.Tools[0].ResponseSchema == nil || app.Tools[2].ResponseSchema != nil {
			t.Fatalf("%s lost responseSchema or opted legacy tool in", name)
		}
	}
	key, err := admin.GetKey(ctx, adminapi.GetKeyParams{AgentInstance: "synthetic-agent", Namespace: "agents", Tier: manifest.TierUser})
	if err != nil {
		t.Fatal(err)
	}
	session := connectHTTPClient(t, server.URL+"/synthetic/mcp", key.Key)
	defer func() { _ = session.Close() }()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed.Tools {
		if tool.Name == "admin_only" {
			t.Fatal("user discovered an admin tool")
		}
	}
	forbidden, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "admin_only"})
	if err != nil || !forbidden.IsError || requests.Load() != 0 {
		t.Fatal("admin call was not rejected before upstream execution")
	}
	for _, key := range []string{"items", "results"} {
		for _, tt := range []struct {
			name    string
			body    string
			status  int
			want    string
			isError bool
		}{
			{"empty", fmt.Sprintf(`{"%s":[],"secret":"synthetic-sensitive-marker"}`, key), 200, "", false},
			{"populated", fmt.Sprintf(`{"%s":[{"name":"synthetic-example","secret":"synthetic-sensitive-marker"}]}`, key), 200, "synthetic-example", false},
			{"missing", `{"secret":"synthetic-sensitive-marker"}`, 200, "", true},
			{"null", fmt.Sprintf(`{"%s":null}`, key), 200, "", true},
			{"object", fmt.Sprintf(`{"%s":{"secret":"synthetic-sensitive-marker"}}`, key), 200, "", true},
			{"string", fmt.Sprintf(`{"%s":"synthetic-sensitive-marker"}`, key), 200, "", true},
			{"number", fmt.Sprintf(`{"%s":17}`, key), 200, "", true},
			{"boolean", fmt.Sprintf(`{"%s":true}`, key), 200, "", true},
			{"null root", `null`, 200, "", true},
			{"array root", `[]`, 200, "", true},
			{"string root", `"synthetic-sensitive-marker"`, 200, "", true},
			{"number root", `17`, 200, "", true},
			{"boolean root", `true`, 200, "", true},
			{"malformed JSON", `{"secret":"synthetic-sensitive-marker"`, 200, "", true},
			{"trailing JSON", fmt.Sprintf(`{"%s":[]} {"secret":"synthetic-sensitive-marker"}`, key), 200, "", true},
			{"empty body", ``, 200, "", true},
			{"no content", ``, 204, "", true},
			{"HTTP failure", `{"secret":"synthetic-sensitive-marker"}`, 403, "Upstream HTTP 403", true},
			{"HTTP failure valid body", fmt.Sprintf(`{"%s":[]}`, key), 500, "Upstream HTTP 500", true},
		} {
			t.Run(key+"/"+tt.name, func(t *testing.T) {
				current.Store(fixture{tt.body, tt.status})
				result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: key, Arguments: map[string]any{}})
				if err != nil {
					t.Fatal(err)
				}
				text := joinedText(result)
				if result.IsError != tt.isError || len(result.Content) != 1 || result.StructuredContent != nil {
					t.Fatalf("unexpected MCP result: error=%t, content blocks=%d", result.IsError, len(result.Content))
				}
				if tt.isError && strings.TrimSpace(text) == "" {
					t.Fatal("MCP error content was empty")
				}
				if (!tt.isError || tt.want != "") && text != tt.want {
					t.Fatalf("MCP text=%q, want %q", text, tt.want)
				}
				encoded, err := json.Marshal(result)
				if err != nil {
					t.Fatal(err)
				}
				for _, marker := range []string{"synthetic-sensitive-marker", "synthetic-header-marker", "synthetic-credential-marker"} {
					if bytes.Contains(encoded, []byte(marker)) || strings.Contains(logs.String(), marker) {
						t.Fatal("MCP result or diagnostics exposed a sensitive marker")
					}
				}
			})
		}
	}
	current.Store(fixture{`{}`, 200})
	legacy, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "legacy"})
	if err != nil || legacy.IsError || joinedText(legacy) != "" {
		t.Fatal("legacy missing collection behavior changed")
	}
	if !strings.Contains(logs.String(), `"outcome":"upstream-call"`) {
		t.Fatal("response validation failures were not recorded as upstream failures")
	}
}
