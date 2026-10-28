package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

func TestRuntimeNativeEndpointAuthenticationDiscoveryAndRotation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	nativeServer := mcp.NewServer(&mcp.Implementation{Name: "native-server", Version: "1.0"}, &mcp.ServerOptions{Instructions: "Native instructions"})
	nativeServer.AddTool(&mcp.Tool{
		Name: "native_operation", Description: "Authoritative upstream description",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","enum":["demo"]}},"required":["name"]}`),
		OutputSchema: json.RawMessage(`{"type":"object","properties":{"reason":{"type":"string"}}}`),
		Meta:         mcp.Meta{"source": "native"},
	}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if !strings.Contains(string(req.Params.Arguments), `"demo"`) {
			t.Error("native arguments were not forwarded")
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: "native refusal"},
				&mcp.ImageContent{Data: []byte("example"), MIMEType: "image/png"},
			},
			StructuredContent: map[string]any{"reason": "native refusal"},
			IsError:           true,
			Meta:              mcp.Meta{"source": "native"},
		}, nil
	})
	upstreamHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return nativeServer }, &mcp.StreamableHTTPOptions{})
	var expectedCredential atomic.Value
	expectedCredential.Store("first-token")
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		if r.URL.Path != "/api/mcp" {
			t.Error("gateway path was not rewritten to the upstream endpoint")
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+expectedCredential.Load().(string) {
			t.Error("gateway did not supply the current server-side credential")
			http.Error(w, "upstream credential rejected", http.StatusUnauthorized)
			return
		}
		upstreamHandler.ServeHTTP(w, r)
	}))
	defer upstream.Close()

	client := kubernetesfake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "native-mcp-keys", Namespace: "mcp2rest"},
		Data:       map[string][]byte{"token": []byte("first-token")},
	})
	table, err := discovery.New(client)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- table.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	store := keys.NewStore()
	gatewayKey, err := store.Mint("agent", manifest.TierUser)
	if err != nil {
		t.Fatal(err)
	}
	adminKey, err := store.Mint("admin", manifest.TierAdmin)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := keys.NewSecretWriter(client)
	if err != nil {
		t.Fatal(err)
	}
	creds := mustCreds(t, client)
	admin, err := adminapi.New(client, table, store, newAgentKeys(t, writer, store), creds)
	if err != nil {
		t.Fatal(err)
	}
	skillGVR := schema.GroupVersionResource{Group: skillstools.Group, Version: skillstools.Version, Resource: skillstools.Resource}
	skillClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		skillGVR: skillstools.Kind + "List",
	})
	metrics, logger, metricsHandler := testRuntimeObservability(t)
	handler, err := NewRuntimeHandler(table, store, render.New(nil), passthrough.New(nil),
		passthrough.NewProxy(upstream.Client().Transport, logger), creds, admin, skillClient, metrics, logger, metricsHandler)
	if err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewServer(handler)
	defer gateway.Close()
	adminSession := connectHTTPClient(t, gateway.URL+"/mcp", adminKey)
	defer func() {
		if err := adminSession.Close(); err != nil {
			t.Error(err)
		}
	}()
	result, err := adminSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "register_app",
		Arguments: map[string]any{
			"namespace": "apps",
			"manifest": map[string]any{
				"name": "native", "upstreamMCPURL": upstream.URL + "/api/mcp", "upstreamCredentialEnv": "token",
			},
		},
	})
	if err != nil || result.IsError {
		t.Fatalf("register native endpoint: result=%+v, error=%v", result, err)
	}
	waitForDiscoveredApp(t, table, "native")

	for _, tt := range []struct {
		key    string
		status int
	}{
		{"", http.StatusUnauthorized},
		{"unknown-key", http.StatusUnauthorized},
		{gatewayKey, http.StatusServiceUnavailable},
	} {
		req := httptest.NewRequest(http.MethodPost, "/native/mcp", strings.NewReader(`{}`))
		if tt.key != "" {
			req.Header.Set("Authorization", "Bearer "+tt.key)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != tt.status {
			t.Fatalf("native endpoint before reload: status=%d, want %d", rec.Code, tt.status)
		}
	}
	if upstreamCalls.Load() != 0 {
		t.Fatal("unauthenticated or unprovisioned request reached upstream")
	}
	reloadNativeCredentials(t, ctx, adminSession)
	session := connectHTTPClient(t, gateway.URL+"/native/mcp", gatewayKey)
	sessionClosed := false
	defer func() {
		if !sessionClosed {
			if err := session.Close(); err != nil {
				t.Error(err)
			}
		}
	}()
	if session.InitializeResult().Instructions != "Native instructions" {
		t.Fatal("native initialize result was replaced by gateway instructions")
	}
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != "native_operation" || tools.Tools[0].Description != "Authoritative upstream description" || tools.Tools[0].OutputSchema == nil || tools.Tools[0].Meta["source"] != "native" {
		t.Fatalf("native tool metadata was reconstructed or mixed with gateway tools: %+v", tools.Tools)
	}
	encodedSchema, err := json.Marshal(tools.Tools[0].InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encodedSchema), `"required":["name"]`) || !strings.Contains(string(encodedSchema), `"enum":["demo"]`) {
		t.Fatalf("native schema was not preserved: %s", encodedSchema)
	}
	callResult, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "native_operation", Arguments: map[string]any{"name": "demo"}})
	if err != nil {
		t.Fatal(err)
	}
	if !callResult.IsError || len(callResult.Content) != 2 || callResult.StructuredContent == nil || callResult.Meta["source"] != "native" {
		t.Fatalf("native result/error fidelity lost: %+v", callResult)
	}
	if _, ok := callResult.Content[1].(*mcp.ImageContent); !ok {
		t.Fatal("native image content was flattened")
	}

	secret, err := client.CoreV1().Secrets("mcp2rest").Get(ctx, "native-mcp-keys", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	secret.Data["token"] = []byte("rotated-token")
	if _, err := client.CoreV1().Secrets("mcp2rest").Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	reloadNativeCredentials(t, ctx, adminSession)
	expectedCredential.Store("rotated-token")
	nativeServer.AddTool(&mcp.Tool{Name: "new_native_operation", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "new"}}}, nil
		})
	tools, err = session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 2 {
		t.Fatalf("native relist after rotation/change: result=%+v, error=%v", tools, err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	sessionClosed = true
	if err := store.Revoke(gatewayKey); err != nil {
		t.Fatal(err)
	}
	before := upstreamCalls.Load()
	req := httptest.NewRequest(http.MethodDelete, "/native/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+gatewayKey)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || upstreamCalls.Load() != before {
		t.Fatal("revoked gateway key reached native upstream")
	}
}

func reloadNativeCredentials(t *testing.T, ctx context.Context, session *mcp.ClientSession) {
	t.Helper()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "reload_cache"})
	if err != nil || result.IsError {
		t.Fatalf("reload_cache: result=%+v, error=%v", result, err)
	}
}

func TestNativeAnonymousUpstreamDoesNotReceiveGatewayCredential(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("gateway credential reached anonymous upstream")
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer upstream.Close()
	req := httptest.NewRequest(http.MethodPost, "/native/mcp", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer gateway-key")
	proxy := passthrough.NewProxy(upstream.Client().Transport, nil)
	rec := httptest.NewRecorder()
	proxy.Forward(rec, req, manifest.App{Name: "native", UpstreamMCPURL: upstream.URL + "/mcp"}, "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("anonymous upstream response: %d %s", rec.Code, rec.Body.String())
	}
}
