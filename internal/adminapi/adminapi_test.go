package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/karlrissland/homelab-api2mcp/internal/discovery"
	"github.com/karlrissland/homelab-api2mcp/internal/keys"
	"github.com/karlrissland/homelab-api2mcp/internal/manifest"
)

func TestEnsureBootstrapAdminKeyPrintsOnce(t *testing.T) {
	t.Parallel()

	api := newTestAPI(t)
	var out bytes.Buffer

	key, created, err := api.EnsureBootstrapAdminKey(&out)
	if err != nil {
		t.Fatalf("EnsureBootstrapAdminKey(first) error = %v", err)
	}
	if !created || key == "" {
		t.Fatalf("EnsureBootstrapAdminKey(first) = (%q, %t), want created non-empty key", key, created)
	}
	if !bytes.Contains(out.Bytes(), []byte(key)) {
		t.Fatalf("bootstrap output did not include key %q: %q", key, out.String())
	}

	key, created, err = api.EnsureBootstrapAdminKey(&out)
	if err != nil {
		t.Fatalf("EnsureBootstrapAdminKey(second) error = %v", err)
	}
	if created || key != "" {
		t.Fatalf("EnsureBootstrapAdminKey(second) = (%q, %t), want no-op", key, created)
	}
	if got := len(api.store.List()); got != 1 {
		t.Fatalf("store.List() length = %d, want 1 bootstrap key", got)
	}
}

func TestRegisterAppCreatesSecretAndIsIdempotent(t *testing.T) {
	t.Parallel()

	api := newTestAPI(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDiscovery(t, api.table, ctx)

	params := RegisterAppParams{
		Namespace: "apps",
		Manifest:  testManifest("demo"),
		AuthorizedAgents: []AuthorizedAgent{
			{AgentInstance: "hermes-alice", Namespace: "agents"},
		},
	}

	result, err := api.RegisterApp(ctx, params)
	if err != nil {
		t.Fatalf("RegisterApp(first) error = %v", err)
	}
	if len(result.CreatedUserKeys) != 1 {
		t.Fatalf("RegisterApp(first) created %d keys, want 1", len(result.CreatedUserKeys))
	}

	secretKeys, err := api.writer.ReadSecretKeys(ctx, "hermes-alice", "agents")
	if err != nil {
		t.Fatalf("ReadSecretKeys() error = %v", err)
	}
	if len(secretKeys) != 1 {
		t.Fatalf("secret keys = %#v, want one user key", secretKeys)
	}

	second, err := api.RegisterApp(ctx, params)
	if err != nil {
		t.Fatalf("RegisterApp(second) error = %v", err)
	}
	if len(second.CreatedUserKeys) != 0 {
		t.Fatalf("RegisterApp(second) created %d keys, want 0", len(second.CreatedUserKeys))
	}

	again, err := api.writer.ReadSecretKeys(ctx, "hermes-alice", "agents")
	if err != nil {
		t.Fatalf("ReadSecretKeys(second) error = %v", err)
	}
	if len(again) != 1 || again[0] != secretKeys[0] {
		t.Fatalf("secret keys after re-register = %#v, want original %#v", again, secretKeys)
	}
}

func TestReloadCacheCallsCredentialReloader(t *testing.T) {
	t.Parallel()

	api := newTestAPI(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDiscovery(t, api.table, ctx)

	if _, err := api.RegisterApp(ctx, RegisterAppParams{Namespace: "apps", Manifest: testManifest("demo")}); err != nil {
		t.Fatalf("RegisterApp() error = %v", err)
	}
	waitForApp(t, api.table, "demo")

	stub, ok := api.creds.(*stubCredentialReloader)
	if !ok {
		t.Fatalf("api.creds = %T, want *stubCredentialReloader", api.creds)
	}

	result, err := api.ReloadCache(ctx)
	if err != nil {
		t.Fatalf("ReloadCache() error = %v", err)
	}
	if result.Apps != 1 {
		t.Fatalf("ReloadCache() Apps = %d, want 1", result.Apps)
	}
	if stub.called != 1 {
		t.Fatalf("CredentialReloader.Reload called %d times, want 1", stub.called)
	}
}

func TestListAppsAndGetManifestReflectDiscovery(t *testing.T) {
	t.Parallel()

	api := newTestAPI(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDiscovery(t, api.table, ctx)

	if _, err := api.RegisterApp(ctx, RegisterAppParams{Namespace: "apps", Manifest: testManifest("demo")}); err != nil {
		t.Fatalf("RegisterApp() error = %v", err)
	}

	waitForApp(t, api.table, "demo")

	apps := api.table.List()
	if len(apps) != 1 || apps[0].Name != "demo" {
		t.Fatalf("table.List() = %#v, want one demo app", apps)
	}

	app, err := api.GetManifest(ctx, GetManifestParams{AppName: "demo"})
	if err != nil {
		t.Fatalf("GetManifest() error = %v", err)
	}
	if app.Name != "demo" || app.Namespace != "apps" {
		t.Fatalf("GetManifest() = %+v, want demo in apps namespace", app)
	}
}

func TestCreateRevokeRotateKeyUpdatesSecret(t *testing.T) {
	t.Parallel()

	api := newTestAPI(t)
	ctx := context.Background()

	created, err := api.CreateKey(ctx, CreateKeyParams{
		AgentInstance: "hermes-alice",
		Namespace:     "agents",
		Tier:          manifest.TierUser,
	})
	if err != nil {
		t.Fatalf("CreateKey() error = %v", err)
	}
	if created.Key == "" {
		t.Fatal("CreateKey() returned empty key")
	}

	keysBefore, err := api.writer.ReadSecretKeys(ctx, "hermes-alice", "agents")
	if err != nil {
		t.Fatalf("ReadSecretKeys(before) error = %v", err)
	}
	if len(keysBefore) != 1 || keysBefore[0] != created.Key {
		t.Fatalf("secret before rotate = %#v, want created key", keysBefore)
	}

	rotated, err := api.RotateKey(ctx, RotateKeyParams{
		AgentInstance: "hermes-alice",
		Namespace:     "agents",
		Tier:          manifest.TierUser,
	})
	if err != nil {
		t.Fatalf("RotateKey() error = %v", err)
	}
	if rotated.Key == "" || rotated.Key == created.Key {
		t.Fatalf("RotateKey() returned key %q, want new non-empty key distinct from %q", rotated.Key, created.Key)
	}
	if rotated.Revoked != 1 {
		t.Fatalf("RotateKey().Revoked = %d, want 1", rotated.Revoked)
	}
	if _, ok := api.store.Lookup(created.Key); ok {
		t.Fatal("rotated old key still present in store")
	}

	keysAfterRotate, err := api.writer.ReadSecretKeys(ctx, "hermes-alice", "agents")
	if err != nil {
		t.Fatalf("ReadSecretKeys(after rotate) error = %v", err)
	}
	if len(keysAfterRotate) != 1 || keysAfterRotate[0] != rotated.Key {
		t.Fatalf("secret after rotate = %#v, want rotated key only", keysAfterRotate)
	}

	revoked, err := api.RevokeKey(ctx, RevokeKeyParams{
		Namespace: "agents",
		RawKey:    rotated.Key,
	})
	if err != nil {
		t.Fatalf("RevokeKey() error = %v", err)
	}
	if revoked.Revoked != 1 {
		t.Fatalf("RevokeKey().Revoked = %d, want 1", revoked.Revoked)
	}
	if _, ok := api.store.Lookup(rotated.Key); ok {
		t.Fatal("revoked key still present in store")
	}
	keysAfterRevoke, err := api.writer.ReadSecretKeys(ctx, "hermes-alice", "agents")
	if err != nil {
		t.Fatalf("ReadSecretKeys(after revoke) error = %v", err)
	}
	if len(keysAfterRevoke) != 0 {
		t.Fatalf("secret after revoke = %#v, want empty", keysAfterRevoke)
	}
}

func TestManagementToolsRejectNonAdminKey(t *testing.T) {
	t.Parallel()

	api := newTestAPI(t)
	userKey, err := api.store.Mint("hermes-alice", manifest.TierUser)
	if err != nil {
		t.Fatalf("Mint(user) error = %v", err)
	}

	server := mcp.NewServer(&mcp.Implementation{Name: "admin-test", Version: "0.0.0"}, nil)
	api.RegisterTools(server, userKey)
	session := connectSession(t, server)
	defer func() { _ = session.Close() }()

	cases := []mcp.CallToolParams{
		{Name: "register_app", Arguments: map[string]any{"namespace": "apps", "manifest": map[string]any{"name": "demo", "tools": []any{map[string]any{"name": "list", "tier": "user", "type": "rendered", "requestTemplate": "{}", "responseTemplate": "{}"}}}}},
		{Name: "deregister_app", Arguments: map[string]any{"namespace": "apps", "appName": "demo"}},
		{Name: "list_apps"},
		{Name: "get_manifest", Arguments: map[string]any{"appName": "demo"}},
		{Name: "create_key", Arguments: map[string]any{"agentInstance": "hermes-alice", "namespace": "agents", "tier": "user"}},
		{Name: "list_keys"},
		{Name: "revoke_key", Arguments: map[string]any{"namespace": "agents", "agentInstance": "hermes-alice", "tier": "user"}},
		{Name: "rotate_key", Arguments: map[string]any{"agentInstance": "hermes-alice", "namespace": "agents", "tier": "user"}},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			result, err := session.CallTool(context.Background(), &tc)
			if err != nil {
				t.Fatalf("CallTool(%s) protocol error = %v", tc.Name, err)
			}
			if !result.IsError {
				t.Fatalf("CallTool(%s) IsError = false, want true", tc.Name)
			}
		})
	}
}

func newTestAPI(t *testing.T) *API {
	t.Helper()

	client := fake.NewSimpleClientset()
	table, err := discovery.New(client)
	if err != nil {
		t.Fatalf("discovery.New() error = %v", err)
	}
	store := keys.NewStore()
	writer, err := keys.NewSecretWriter(client)
	if err != nil {
		t.Fatalf("keys.NewSecretWriter() error = %v", err)
	}
	api, err := New(client, table, store, writer, &stubCredentialReloader{})
	if err != nil {
		t.Fatalf("adminapi.New() error = %v", err)
	}
	return api
}

type stubCredentialReloader struct {
	called int
	err    error
}

func (s *stubCredentialReloader) Reload(context.Context, []*manifest.App) error {
	s.called++
	return s.err
}

func runDiscovery(t *testing.T, table *discovery.Table, ctx context.Context) {
	t.Helper()

	errCh := make(chan error, 1)
	go func() {
		errCh <- table.Run(ctx)
	}()
	t.Cleanup(func() {
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("table.Run() error = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for discovery table shutdown")
		}
	})
}

func waitForApp(t *testing.T, table *discovery.Table, appName string) {
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

func testManifest(name string) manifest.App {
	return manifest.App{
		Name:            name,
		UpstreamBaseURL: "https://example.invalid",
		Tools: []manifest.Tool{
			{
				Name:             "list_repos",
				Description:      "List repos",
				Tier:             manifest.TierUser,
				Type:             manifest.ToolTypeRendered,
				RequestTemplate:  `{"method":"GET","path":"/repos"}`,
				ResponseTemplate: `{{ response.rawBody }}`,
			},
		},
	}
}

func connectSession(t *testing.T, server *mcp.Server) *mcp.ClientSession {
	t.Helper()

	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()

	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server.Connect() error = %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect() error = %v", err)
	}
	return session
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

func TestRegisterToolRoundTrip(t *testing.T) {
	t.Parallel()

	api := newTestAPI(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDiscovery(t, api.table, ctx)

	adminKey, err := api.store.Mint("cluster-admin", manifest.TierAdmin)
	if err != nil {
		t.Fatalf("Mint(admin) error = %v", err)
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "admin-test", Version: "0.0.0"}, nil)
	api.RegisterTools(server, adminKey)
	session := connectSession(t, server)
	defer func() { _ = session.Close() }()

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "register_app",
		Arguments: map[string]any{
			"namespace": "apps",
			"manifest": map[string]any{
				"name":            "demo",
				"upstreamBaseURL": "https://example.invalid",
				"tools": []any{
					map[string]any{
						"name":             "list_repos",
						"description":      "List repos",
						"tier":             "user",
						"type":             "rendered",
						"requestTemplate":  `{"method":"GET","path":"/repos"}`,
						"responseTemplate": `{{ response.rawBody }}`,
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
	if result.IsError {
		t.Fatalf("CallTool(register_app) returned tool error: %s", textContent(t, result))
	}

	decoded := decodeStructured[RegisterAppResult](t, result.StructuredContent)
	if decoded.AppName != "demo" || decoded.ConfigMapName == "" {
		t.Fatalf("register_app structured result = %+v", decoded)
	}

	waitForApp(t, api.table, "demo")

	listResult, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_apps"})
	if err != nil {
		t.Fatalf("CallTool(list_apps) error = %v", err)
	}
	if listResult.IsError {
		t.Fatalf("list_apps tool error: %s", textContent(t, listResult))
	}
	apps := decodeStructured[[]manifest.App](t, listResult.StructuredContent)
	if len(apps) != 1 || apps[0].Name != "demo" {
		t.Fatalf("list_apps structured result = %+v, want one demo app", apps)
	}

	manifestResult, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "get_manifest",
		Arguments: map[string]any{"appName": "demo"},
	})
	if err != nil {
		t.Fatalf("CallTool(get_manifest) error = %v", err)
	}
	if manifestResult.IsError {
		t.Fatalf("get_manifest tool error: %s", textContent(t, manifestResult))
	}
	gotManifest := decodeStructured[manifest.App](t, manifestResult.StructuredContent)
	if gotManifest.Name != "demo" || gotManifest.Namespace != "apps" {
		t.Fatalf("get_manifest structured result = %+v, want demo in apps namespace", gotManifest)
	}
}

func textContent(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()

	var parts []string
	for _, item := range result.Content {
		if text, ok := item.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}
