package adminapi

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/karlrissland/homelab-api2mcp/internal/discovery"
	"github.com/karlrissland/homelab-api2mcp/internal/keys"
	"github.com/karlrissland/homelab-api2mcp/internal/manifest"
)

const bootstrapAdminAgent = "cluster-admin"

// CredentialReloader rebuilds the upstream credential cache
// (internal/upstreamcreds) from the currently known app list. It is a
// narrow interface so adminapi does not need to import upstreamcreds
// directly, matching this package's existing dependency-inversion style.
type CredentialReloader interface {
	Reload(ctx context.Context, apps []*manifest.App) error
}

// API serves the reserved management tool set.
type API struct {
	client kubernetes.Interface
	table  *discovery.Table
	store  *keys.Store
	writer *keys.SecretWriter
	creds  CredentialReloader
}

// AuthorizedAgent identifies one agent instance that should hold a key.
//
// Tier is a property of the agent instance itself (e.g. hlctl's Admin-tier
// Cluster Agent singleton), not of the registering app's own manifest tool
// tiers -- register_app mints/ensures a key matching this field directly,
// independent of whatever tool tiers the app declares (homelab-api2mcp#4).
// Tier is optional and defaults to manifest.TierUser for backward
// compatibility with callers sent before this field existed.
type AuthorizedAgent struct {
	AgentInstance string        `json:"agentInstance" jsonschema:"Agent instance name"`
	Namespace     string        `json:"namespace" jsonschema:"Namespace holding the agent Secret"`
	Tier          manifest.Tier `json:"tier,omitempty" jsonschema:"Tier the agent instance itself holds: user or admin; defaults to user if omitted"`
}

// RegisterAppParams declares a management-tool registration call.
type RegisterAppParams struct {
	Namespace        string            `json:"namespace" jsonschema:"Namespace owning the app ConfigMap"`
	ConfigMapName    string            `json:"configMapName,omitempty" jsonschema:"Optional explicit ConfigMap name; defaults to <app>-mcp2rest"`
	Manifest         manifest.App      `json:"manifest" jsonschema:"Full manifest.App payload to validate and persist"`
	AuthorizedAgents []AuthorizedAgent `json:"authorizedAgents,omitempty" jsonschema:"Agent instances that must each hold a key matching their own declared tier"`
}

// RegisterAppResult reports what register_app changed.
type RegisterAppResult struct {
	AppName       string      `json:"appName"`
	Namespace     string      `json:"namespace"`
	ConfigMapName string      `json:"configMapName"`
	CreatedKeys   []KeyRecord `json:"createdKeys"`
}

// ListAppsResult wraps list_apps' array payload in an object. The MCP
// spec requires every tool outputSchema to be an object schema even when
// the primary payload is array-shaped -- a bare array/null union at the
// top level fails strict client-side schema validation and can abort an
// MCP client's entire connection to this server, not just this one tool
// (see homelab-api2mcp#3).
type ListAppsResult struct {
	Apps []*manifest.App `json:"apps"`
}

// ListKeysResult wraps list_keys' array payload in an object, for the
// same reason as ListAppsResult (homelab-api2mcp#3).
type ListKeysResult struct {
	Keys []KeyRecord `json:"keys"`
}

// DeregisterAppParams removes an app's registration ConfigMap.
type DeregisterAppParams struct {
	Namespace     string `json:"namespace" jsonschema:"Namespace owning the app ConfigMap"`
	AppName       string `json:"appName" jsonschema:"Registered app name"`
	ConfigMapName string `json:"configMapName,omitempty" jsonschema:"Optional explicit ConfigMap name; defaults to <app>-mcp2rest"`
}

// DeregisterAppResult reports whether the app ConfigMap existed.
type DeregisterAppResult struct {
	AppName       string `json:"appName"`
	Namespace     string `json:"namespace"`
	ConfigMapName string `json:"configMapName"`
	Removed       bool   `json:"removed"`
}

// GetManifestParams fetches one app manifest from discovery.
type GetManifestParams struct {
	AppName string `json:"appName" jsonschema:"Registered app name"`
}

// CreateKeyParams mints a key and writes the owning Secret.
type CreateKeyParams struct {
	AgentInstance string        `json:"agentInstance" jsonschema:"Agent instance name"`
	Namespace     string        `json:"namespace" jsonschema:"Namespace holding the key Secret"`
	Tier          manifest.Tier `json:"tier" jsonschema:"Key tier: user or admin"`
}

// KeyRecord is the management-tool view of one key.
type KeyRecord struct {
	AgentInstance string        `json:"agentInstance"`
	Tier          manifest.Tier `json:"tier"`
	CreatedAt     string        `json:"createdAt"`
}

// CreateKeyResult returns raw key material once, at creation time only.
type CreateKeyResult struct {
	AgentInstance string        `json:"agentInstance"`
	Namespace     string        `json:"namespace"`
	Tier          manifest.Tier `json:"tier"`
	Key           string        `json:"key"`
}

// GetKeyParams requests the current raw key for one agent instance+tier.
type GetKeyParams struct {
	AgentInstance string        `json:"agentInstance" jsonschema:"Agent instance name"`
	Namespace     string        `json:"namespace" jsonschema:"Namespace holding the key Secret"`
	Tier          manifest.Tier `json:"tier" jsonschema:"Tier to fetch: user or admin"`
}

// GetKeyResult returns the current raw key material for one agent+tier.
//
// This is a narrow, deliberate exception to mcp2rest's normal "raw key
// material never leaves mcp2rest except at mint time" posture (see
// docs/decisions/mcp2rest-plan.md's "hlctl never touches raw key material"
// principle). It exists only for callers that must bake a literal
// credential value into another system's own static config at deploy time
// (e.g. hlctl writing HLCTL_MCP_AUTH_HEADER_VALUE for OpenClaw's
// provision-mcp-server hook, which has no indirect secret-reference
// mechanism of its own) and still requires the caller to hold mcp2rest's
// own admin scope.
type GetKeyResult struct {
	AgentInstance string        `json:"agentInstance"`
	Namespace     string        `json:"namespace"`
	Tier          manifest.Tier `json:"tier"`
	Key           string        `json:"key"`
}

// RevokeKeyParams revokes either one raw key or every key matching an
// agent+tier in the target Secret.
type RevokeKeyParams struct {
	Namespace     string        `json:"namespace" jsonschema:"Namespace holding the key Secret"`
	AgentInstance string        `json:"agentInstance,omitempty" jsonschema:"Agent instance name (required unless rawKey is provided)"`
	Tier          manifest.Tier `json:"tier,omitempty" jsonschema:"Tier to revoke when revoking by agentInstance+tier"`
	RawKey        string        `json:"rawKey,omitempty" jsonschema:"Raw key to revoke directly"`
}

// RevokeKeyResult reports how many keys were revoked.
type RevokeKeyResult struct {
	AgentInstance string `json:"agentInstance"`
	Namespace     string `json:"namespace"`
	Revoked       int    `json:"revoked"`
}

// RotateKeyParams rotates every key for one agent+tier.
type RotateKeyParams struct {
	AgentInstance string        `json:"agentInstance" jsonschema:"Agent instance name"`
	Namespace     string        `json:"namespace" jsonschema:"Namespace holding the key Secret"`
	Tier          manifest.Tier `json:"tier" jsonschema:"Tier to rotate"`
}

// RotateKeyResult reports the new key and how many old keys were revoked.
type RotateKeyResult struct {
	AgentInstance string        `json:"agentInstance"`
	Namespace     string        `json:"namespace"`
	Tier          manifest.Tier `json:"tier"`
	Key           string        `json:"key"`
	Revoked       int           `json:"revoked"`
}

// ReloadCacheResult reports how many apps' credentials were reloaded.
type ReloadCacheResult struct {
	Apps int `json:"apps"`
}

// New constructs the management API.
func New(client kubernetes.Interface, table *discovery.Table, store *keys.Store, writer *keys.SecretWriter, creds CredentialReloader) (*API, error) {
	switch {
	case client == nil:
		return nil, fmt.Errorf("new admin api: kubernetes client is required")
	case table == nil:
		return nil, fmt.Errorf("new admin api: discovery table is required")
	case store == nil:
		return nil, fmt.Errorf("new admin api: key store is required")
	case writer == nil:
		return nil, fmt.Errorf("new admin api: secret writer is required")
	case creds == nil:
		return nil, fmt.Errorf("new admin api: credential reloader is required")
	default:
		return &API{client: client, table: table, store: store, writer: writer, creds: creds}, nil
	}
}

// EnsureBootstrapAdminKey mints the one bootstrap admin key once per process
// lifetime and writes it only to the provided output stream for this v1 phase.
func (a *API) EnsureBootstrapAdminKey(w io.Writer) (string, bool, error) {
	if w == nil {
		return "", false, fmt.Errorf("bootstrap admin key: output writer is required")
	}
	for _, record := range a.store.List() {
		if record.AgentInstance == bootstrapAdminAgent && record.Tier == manifest.TierAdmin {
			return "", false, nil
		}
	}

	key, err := a.store.Mint(bootstrapAdminAgent, manifest.TierAdmin)
	if err != nil {
		return "", false, fmt.Errorf("bootstrap admin key: %w", err)
	}

	// v1 intentionally delivers the bootstrap key only to process output so it
	// is impossible to miss but not broadly persisted in-cluster.
	if _, err := fmt.Fprintf(w, "\n===== MCP2REST BOOTSTRAP ADMIN KEY =====\n%s\n========================================\n", key); err != nil {
		return "", false, fmt.Errorf("bootstrap admin key: write banner: %w", err)
	}

	return key, true, nil
}

// RegisterTools adds the reserved management tools to server, authorizing
// every call with the provided raw API key.
func (a *API) RegisterTools(server *mcp.Server, apiKey string) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "register_app",
		Description: "Validate a manifest, upsert its discovery ConfigMap, and ensure user-tier keys exist for each authorized agent instance.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"namespace":     map[string]any{"type": "string"},
				"configMapName": map[string]any{"type": "string"},
				"manifest":      map[string]any{"type": "object"},
				"authorizedAgents": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"agentInstance": map[string]any{"type": "string"},
							"namespace":     map[string]any{"type": "string"},
						},
						"required": []string{"agentInstance", "namespace"},
					},
				},
			},
			"required": []string{"namespace", "manifest"},
		},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, params RegisterAppParams) (*mcp.CallToolResult, RegisterAppResult, error) {
		if err := a.requireAdmin(apiKey); err != nil {
			return nil, RegisterAppResult{}, err
		}
		result, err := a.RegisterApp(ctx, params)
		return nil, result, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "deregister_app",
		Description: "Delete one app registration ConfigMap. Existing keys remain valid until manually revoked.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, params DeregisterAppParams) (*mcp.CallToolResult, DeregisterAppResult, error) {
		if err := a.requireAdmin(apiKey); err != nil {
			return nil, DeregisterAppResult{}, err
		}
		result, err := a.DeregisterApp(ctx, params)
		return nil, result, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "list_apps",
		Description: "List every app registered with mcp2rest, from the live discovery table. Admin tier " +
			"required. Returns app names and their declared tools/schemas only -- it does NOT return a " +
			"connection URL or any API key, and calling a listed tool name directly on THIS session will " +
			"fail (these tools do not exist here). Each returned app includes mcpEndpointPath (e.g. " +
			"\"/metube/mcp\"); the full external URL is https://mcp2rest.<dns-zone>{mcpEndpointPath}. A " +
			"human operator must add that as a NEW, separate MCP server entry in your client/harness, " +
			"authenticated with that agent instance's own key -- there is no tool call, here or anywhere, " +
			"that adds it for you.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, ListAppsResult, error) {
		if err := a.requireAdmin(apiKey); err != nil {
			return nil, ListAppsResult{}, err
		}
		apps := a.table.List()
		result := make([]*manifest.App, len(apps))
		for i, app := range apps {
			// Copy before mutating -- app points at the discovery
			// table's own stored object, shared across requests.
			copied := *app
			copied.MCPEndpointPath = mcpEndpointPath(copied.Name)
			result[i] = &copied
		}
		return nil, ListAppsResult{Apps: result}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_manifest",
		Description: "Fetch one app's full internal manifest (tool names, descriptions, input schemas, " +
			"tiers) by app name. Admin tier required. This is read-only documentation of what that app's " +
			"own MCP endpoint will expose -- it does NOT register, connect, or add the tool to any agent " +
			"or harness, and does NOT return a connection URL or API key. Calling one of the returned " +
			"tool names (e.g. queue_download) directly on THIS session will fail -- those tools do not " +
			"exist here. The result includes mcpEndpointPath (e.g. \"/metube/mcp\"); the full external URL " +
			"is https://mcp2rest.<dns-zone>{mcpEndpointPath}. A human operator must add that as a NEW, " +
			"separate MCP client/server entry, authenticated with that agent instance's own key from its " +
			"<agent-instance>-mcp2rest-keys Secret -- there is no tool call, here or anywhere, that adds " +
			"or connects it for you.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, params GetManifestParams) (*mcp.CallToolResult, manifest.App, error) {
		if err := a.requireAdmin(apiKey); err != nil {
			return nil, manifest.App{}, err
		}
		app, err := a.GetManifest(ctx, params)
		return nil, app, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "create_key",
		Description: "Mint a user/admin key for one agent instance and update that instance's Secret.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, params CreateKeyParams) (*mcp.CallToolResult, CreateKeyResult, error) {
		if err := a.requireAdmin(apiKey); err != nil {
			return nil, CreateKeyResult{}, err
		}
		result, err := a.CreateKey(ctx, params)
		return nil, result, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_keys",
		Description: "List known key records. Raw key material is never returned after mint time.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, ListKeysResult, error) {
		if err := a.requireAdmin(apiKey); err != nil {
			return nil, ListKeysResult{}, err
		}
		return nil, ListKeysResult{Keys: a.ListKeys(ctx)}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_key",
		Description: "Fetch the current raw key for one agent instance+tier. Admin tier required. " +
			"Narrow exception to mcp2rest's normal key-delivery model, for callers (e.g. hlctl) that must " +
			"bake a literal credential value into another system's own static config at deploy time.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, params GetKeyParams) (*mcp.CallToolResult, GetKeyResult, error) {
		if err := a.requireAdmin(apiKey); err != nil {
			return nil, GetKeyResult{}, err
		}
		result, err := a.GetKey(ctx, params)
		return nil, result, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "revoke_key",
		Description: "Revoke either one raw key or every key matching an agent instance and tier.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, params RevokeKeyParams) (*mcp.CallToolResult, RevokeKeyResult, error) {
		if err := a.requireAdmin(apiKey); err != nil {
			return nil, RevokeKeyResult{}, err
		}
		result, err := a.RevokeKey(ctx, params)
		return nil, result, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "rotate_key",
		Description: "Mint a replacement key for one agent instance+tier, update its Secret, and revoke the prior key(s).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, params RotateKeyParams) (*mcp.CallToolResult, RotateKeyResult, error) {
		if err := a.requireAdmin(apiKey); err != nil {
			return nil, RotateKeyResult{}, err
		}
		result, err := a.RotateKey(ctx, params)
		return nil, result, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "reload_cache",
		Description: "Reload the in-memory upstream credential cache from the current discovery table. Call after provisioning or rotating an app's api-keys/mcp-keys Secret.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, ReloadCacheResult, error) {
		if err := a.requireAdmin(apiKey); err != nil {
			return nil, ReloadCacheResult{}, err
		}
		result, err := a.ReloadCache(ctx)
		return nil, result, err
	})
}

// RegisterApp validates and persists one manifest and ensures each authorized
// agent instance holds a key matching its own declared tier (defaulting to
// user when omitted), independent of which tool tiers this app's manifest
// happens to declare (homelab-api2mcp#4).
func (a *API) RegisterApp(ctx context.Context, params RegisterAppParams) (RegisterAppResult, error) {
	if strings.TrimSpace(params.Namespace) == "" {
		return RegisterAppResult{}, fmt.Errorf("register app: namespace is required")
	}
	app := params.Manifest
	if err := app.Validate(); err != nil {
		return RegisterAppResult{}, fmt.Errorf("register app: invalid manifest: %w", err)
	}

	configMapName := params.ConfigMapName
	if configMapName == "" {
		configMapName = defaultConfigMapName(app.Name)
	}
	cm, err := discovery.BuildConfigMap(params.Namespace, configMapName, app)
	if err != nil {
		return RegisterAppResult{}, fmt.Errorf("register app %q: build configmap: %w", app.Name, err)
	}
	if err := a.upsertConfigMap(ctx, cm); err != nil {
		return RegisterAppResult{}, fmt.Errorf("register app %q: upsert configmap: %w", app.Name, err)
	}

	createdKeys := make([]KeyRecord, 0, len(params.AuthorizedAgents))
	for _, agent := range params.AuthorizedAgents {
		tier := agent.Tier
		if tier == "" {
			tier = manifest.TierUser
		}
		if !tier.Valid() {
			return RegisterAppResult{}, fmt.Errorf("register app %q: agent %s/%s: invalid tier %q", app.Name, agent.Namespace, agent.AgentInstance, agent.Tier)
		}
		key, created, err := a.ensureTierKey(ctx, agent.AgentInstance, agent.Namespace, tier)
		if err != nil {
			return RegisterAppResult{}, fmt.Errorf("register app %q: ensure key for %s/%s: %w", app.Name, agent.Namespace, agent.AgentInstance, err)
		}
		if created {
			record, _ := a.store.Lookup(key)
			createdKeys = append(createdKeys, formatKeyRecord(record))
		}
	}

	return RegisterAppResult{
		AppName:       app.Name,
		Namespace:     params.Namespace,
		ConfigMapName: configMapName,
		CreatedKeys:   createdKeys,
	}, nil
}

// DeregisterApp removes one registration ConfigMap. Existing keys are left in
// place until individually revoked by an admin.
func (a *API) DeregisterApp(ctx context.Context, params DeregisterAppParams) (DeregisterAppResult, error) {
	if strings.TrimSpace(params.Namespace) == "" {
		return DeregisterAppResult{}, fmt.Errorf("deregister app: namespace is required")
	}
	if strings.TrimSpace(params.AppName) == "" {
		return DeregisterAppResult{}, fmt.Errorf("deregister app: appName is required")
	}

	configMapName := params.ConfigMapName
	if configMapName == "" {
		configMapName = defaultConfigMapName(params.AppName)
	}

	err := a.client.CoreV1().ConfigMaps(params.Namespace).Delete(ctx, configMapName, metav1.DeleteOptions{})
	removed := true
	if err != nil {
		if apierrors.IsNotFound(err) {
			removed = false
		} else {
			return DeregisterAppResult{}, fmt.Errorf("deregister app %q: delete configmap: %w", params.AppName, err)
		}
	}

	return DeregisterAppResult{
		AppName:       params.AppName,
		Namespace:     params.Namespace,
		ConfigMapName: configMapName,
		Removed:       removed,
	}, nil
}

// mcpEndpointPath returns an app's own mcp2rest-proxied MCP endpoint
// path, e.g. "/metube/mcp" -- always "/" + app name + "/mcp", mirroring
// runtime.go's routing convention. Deliberately generic/app-name-driven,
// never hard-coded to any single app.
func mcpEndpointPath(appName string) string {
	return "/" + appName + "/mcp"
}

// GetManifest returns the live manifest from discovery.
func (a *API) GetManifest(_ context.Context, params GetManifestParams) (manifest.App, error) {
	if strings.TrimSpace(params.AppName) == "" {
		return manifest.App{}, fmt.Errorf("get manifest: appName is required")
	}
	app, ok := a.table.Get(params.AppName)
	if !ok {
		return manifest.App{}, fmt.Errorf("get manifest: app %q not found", params.AppName)
	}
	result := *app
	result.MCPEndpointPath = mcpEndpointPath(result.Name)
	return result, nil
}

// ReloadCache rebuilds the in-memory upstream credential cache from the
// apps currently visible in the live discovery table.
func (a *API) ReloadCache(ctx context.Context) (ReloadCacheResult, error) {
	apps := a.table.List()
	if err := a.creds.Reload(ctx, apps); err != nil {
		return ReloadCacheResult{}, fmt.Errorf("reload cache: %w", err)
	}
	return ReloadCacheResult{Apps: len(apps)}, nil
}

// CreateKey mints and persists a new key for one agent instance.
func (a *API) CreateKey(ctx context.Context, params CreateKeyParams) (CreateKeyResult, error) {
	if err := validateAgentTierParams(params.AgentInstance, params.Namespace, params.Tier, "create key"); err != nil {
		return CreateKeyResult{}, err
	}

	key, err := a.appendTierKey(ctx, params.AgentInstance, params.Namespace, params.Tier)
	if err != nil {
		return CreateKeyResult{}, fmt.Errorf("create key for %s/%s: %w", params.Namespace, params.AgentInstance, err)
	}

	return CreateKeyResult{
		AgentInstance: params.AgentInstance,
		Namespace:     params.Namespace,
		Tier:          params.Tier,
		Key:           key,
	}, nil
}

// ListKeys returns every known key record without raw key material.
func (a *API) ListKeys(context.Context) []KeyRecord {
	records := a.store.List()
	out := make([]KeyRecord, 0, len(records))
	for _, record := range records {
		out = append(out, formatKeyRecord(record))
	}
	return out
}

// GetKey returns the current raw key for one agent instance+tier, resolved
// by cross-referencing the Secret's raw key array against mcp2rest's own
// in-memory key store (the same resolution ensureTierKey already performs
// internally) rather than relying on Secret array ordering or shape.
func (a *API) GetKey(ctx context.Context, params GetKeyParams) (GetKeyResult, error) {
	if err := validateAgentTierParams(params.AgentInstance, params.Namespace, params.Tier, "get key"); err != nil {
		return GetKeyResult{}, err
	}

	rawKeys, err := a.writer.ReadSecretKeys(ctx, params.AgentInstance, params.Namespace)
	if err != nil {
		return GetKeyResult{}, fmt.Errorf("get key for %s/%s: read secret keys: %w", params.Namespace, params.AgentInstance, err)
	}

	for _, rawKey := range rawKeys {
		record, ok := a.store.Lookup(rawKey)
		if ok && record.AgentInstance == params.AgentInstance && record.Tier == params.Tier {
			return GetKeyResult{
				AgentInstance: params.AgentInstance,
				Namespace:     params.Namespace,
				Tier:          params.Tier,
				Key:           rawKey,
			}, nil
		}
	}

	return GetKeyResult{}, fmt.Errorf("get key for %s/%s: no %s-tier key found", params.Namespace, params.AgentInstance, params.Tier)
}

// RevokeKey removes one or more keys and updates the owning Secret.
func (a *API) RevokeKey(ctx context.Context, params RevokeKeyParams) (RevokeKeyResult, error) {
	if strings.TrimSpace(params.Namespace) == "" {
		return RevokeKeyResult{}, fmt.Errorf("revoke key: namespace is required")
	}
	if params.RawKey == "" && strings.TrimSpace(params.AgentInstance) == "" {
		return RevokeKeyResult{}, fmt.Errorf("revoke key: agentInstance is required when rawKey is not provided")
	}
	if params.RawKey == "" && !params.Tier.Valid() {
		return RevokeKeyResult{}, fmt.Errorf("revoke key for agent instance %q: invalid tier %q", params.AgentInstance, params.Tier)
	}

	agentInstance := params.AgentInstance
	if params.RawKey != "" {
		record, ok := a.store.Lookup(params.RawKey)
		if !ok {
			return RevokeKeyResult{}, fmt.Errorf("revoke key: raw key not found")
		}
		agentInstance = record.AgentInstance
	}

	rawKeys, err := a.writer.ReadSecretKeys(ctx, agentInstance, params.Namespace)
	if err != nil {
		return RevokeKeyResult{}, fmt.Errorf("revoke key for %s/%s: read secret keys: %w", params.Namespace, agentInstance, err)
	}

	kept := make([]string, 0, len(rawKeys))
	revoked := 0
	for _, rawKey := range rawKeys {
		record, ok := a.store.Lookup(rawKey)
		if !ok {
			kept = append(kept, rawKey)
			continue
		}
		match := false
		if params.RawKey != "" {
			match = rawKey == params.RawKey
		} else {
			match = record.AgentInstance == agentInstance && record.Tier == params.Tier
		}
		if !match {
			kept = append(kept, rawKey)
			continue
		}
		if err := a.store.Revoke(rawKey); err != nil {
			return RevokeKeyResult{}, fmt.Errorf("revoke key for %s/%s: revoke raw key: %w", params.Namespace, agentInstance, err)
		}
		revoked++
	}
	if revoked == 0 {
		return RevokeKeyResult{}, fmt.Errorf("revoke key for %s/%s: no matching keys found", params.Namespace, agentInstance)
	}
	if err := a.writeSecretKeys(ctx, agentInstance, params.Namespace, kept); err != nil {
		return RevokeKeyResult{}, fmt.Errorf("revoke key for %s/%s: update secret: %w", params.Namespace, agentInstance, err)
	}

	return RevokeKeyResult{
		AgentInstance: agentInstance,
		Namespace:     params.Namespace,
		Revoked:       revoked,
	}, nil
}

// RotateKey replaces every existing key for one agent instance+tier with one
// newly minted key.
func (a *API) RotateKey(ctx context.Context, params RotateKeyParams) (RotateKeyResult, error) {
	if err := validateAgentTierParams(params.AgentInstance, params.Namespace, params.Tier, "rotate key"); err != nil {
		return RotateKeyResult{}, err
	}

	rawKeys, err := a.writer.ReadSecretKeys(ctx, params.AgentInstance, params.Namespace)
	if err != nil {
		return RotateKeyResult{}, fmt.Errorf("rotate key for %s/%s: read secret keys: %w", params.Namespace, params.AgentInstance, err)
	}

	kept := make([]string, 0, len(rawKeys)+1)
	revoked := 0
	for _, rawKey := range rawKeys {
		record, ok := a.store.Lookup(rawKey)
		if !ok {
			kept = append(kept, rawKey)
			continue
		}
		if record.AgentInstance == params.AgentInstance && record.Tier == params.Tier {
			if err := a.store.Revoke(rawKey); err != nil {
				return RotateKeyResult{}, fmt.Errorf("rotate key for %s/%s: revoke raw key: %w", params.Namespace, params.AgentInstance, err)
			}
			revoked++
			continue
		}
		kept = append(kept, rawKey)
	}

	key, err := a.store.Mint(params.AgentInstance, params.Tier)
	if err != nil {
		return RotateKeyResult{}, fmt.Errorf("rotate key for %s/%s: mint replacement key: %w", params.Namespace, params.AgentInstance, err)
	}
	kept = append(kept, key)
	if err := a.writeSecretKeys(ctx, params.AgentInstance, params.Namespace, kept); err != nil {
		return RotateKeyResult{}, fmt.Errorf("rotate key for %s/%s: update secret: %w", params.Namespace, params.AgentInstance, err)
	}

	return RotateKeyResult{
		AgentInstance: params.AgentInstance,
		Namespace:     params.Namespace,
		Tier:          params.Tier,
		Key:           key,
		Revoked:       revoked,
	}, nil
}

func (a *API) requireAdmin(apiKey string) error {
	// Look up before checking for an empty key: when the store has
	// auth disabled (MCP2REST_DISABLE_AUTH), Lookup succeeds
	// unconditionally, including for an empty key. Checking apiKey == ""
	// first would bypass that and always reject no-key callers even
	// with auth disabled.
	record, ok := a.store.Lookup(apiKey)
	if !ok {
		if apiKey == "" {
			return fmt.Errorf("admin key is required")
		}
		return fmt.Errorf("admin key is invalid")
	}
	if !record.Tier.Satisfies(manifest.TierAdmin) {
		return fmt.Errorf("admin tier is required")
	}
	return nil
}

func (a *API) ensureTierKey(ctx context.Context, agentInstance, namespace string, tier manifest.Tier) (string, bool, error) {
	if err := validateAgentTierParams(agentInstance, namespace, tier, "ensure key"); err != nil {
		return "", false, err
	}

	rawKeys, err := a.writer.ReadSecretKeys(ctx, agentInstance, namespace)
	if err != nil {
		return "", false, fmt.Errorf("read existing secret keys: %w", err)
	}
	for _, rawKey := range rawKeys {
		record, ok := a.store.Lookup(rawKey)
		if ok && record.AgentInstance == agentInstance && record.Tier == tier {
			return rawKey, false, nil
		}
	}

	key, err := a.store.Mint(agentInstance, tier)
	if err != nil {
		return "", false, fmt.Errorf("mint key: %w", err)
	}
	rawKeys = append(rawKeys, key)
	if err := a.writer.UpsertSecret(ctx, agentInstance, namespace, rawKeys); err != nil {
		return "", false, fmt.Errorf("write secret: %w", err)
	}
	return key, true, nil
}

func (a *API) appendTierKey(ctx context.Context, agentInstance, namespace string, tier manifest.Tier) (string, error) {
	rawKeys, err := a.writer.ReadSecretKeys(ctx, agentInstance, namespace)
	if err != nil {
		return "", fmt.Errorf("read existing secret keys: %w", err)
	}
	key, err := a.store.Mint(agentInstance, tier)
	if err != nil {
		return "", fmt.Errorf("mint key: %w", err)
	}
	rawKeys = append(rawKeys, key)
	if err := a.writer.UpsertSecret(ctx, agentInstance, namespace, rawKeys); err != nil {
		return "", fmt.Errorf("write secret: %w", err)
	}
	return key, nil
}

func (a *API) upsertConfigMap(ctx context.Context, cm *corev1.ConfigMap) error {
	configMaps := a.client.CoreV1().ConfigMaps(cm.Namespace)
	existing, err := configMaps.Get(ctx, cm.Name, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		_, err = configMaps.Create(ctx, cm, metav1.CreateOptions{})
		return err
	}

	existing.Labels = cm.Labels
	existing.Data = cm.Data
	_, err = configMaps.Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

func (a *API) writeSecretKeys(ctx context.Context, agentInstance, namespace string, rawKeys []string) error {
	if len(rawKeys) == 0 {
		return a.writer.DeleteSecret(ctx, agentInstance, namespace)
	}
	slices.Sort(rawKeys)
	return a.writer.UpsertSecret(ctx, agentInstance, namespace, rawKeys)
}

func defaultConfigMapName(appName string) string {
	return fmt.Sprintf("%s-mcp2rest", appName)
}

func formatKeyRecord(record keys.Record) KeyRecord {
	return KeyRecord{
		AgentInstance: record.AgentInstance,
		Tier:          record.Tier,
		CreatedAt:     record.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

func validateAgentTierParams(agentInstance, namespace string, tier manifest.Tier, verb string) error {
	switch {
	case strings.TrimSpace(agentInstance) == "":
		return fmt.Errorf("%s: agentInstance is required", verb)
	case strings.TrimSpace(namespace) == "":
		return fmt.Errorf("%s for agent instance %q: namespace is required", verb, agentInstance)
	case !tier.Valid():
		return fmt.Errorf("%s for agent instance %q: invalid tier %q", verb, agentInstance, tier)
	default:
		return nil
	}
}
