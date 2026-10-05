package mcpserver

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/client-go/dynamic"

	"github.com/karlrissland/homelab-api2mcp/internal/adminapi"
	"github.com/karlrissland/homelab-api2mcp/internal/discovery"
	"github.com/karlrissland/homelab-api2mcp/internal/keys"
	"github.com/karlrissland/homelab-api2mcp/internal/manifest"
	"github.com/karlrissland/homelab-api2mcp/internal/pipeline"
	"github.com/karlrissland/homelab-api2mcp/internal/render"
	"github.com/karlrissland/homelab-api2mcp/internal/skillstools"
)

const managementPath = "/mcp"
const metricsPath = "/metrics"

// callerUsernameArgument is a reserved top-level tool argument used to
// carry the end-user identity an agent is acting on behalf of for one
// call. We intentionally use a namespaced argument instead of MCP
// request `_meta`: `_meta` is protocol-level transport metadata, while
// agents and generic MCP clients already know how to send tool
// arguments. The runtime strips this key back out before exposing args.*
// to Liquid so app manifests only see their declared tool parameters.
const callerUsernameArgument = "__mcp2rest_caller_username"

type runtimeHandler struct {
	table    *discovery.Table
	store    *keys.Store
	renderer *render.Renderer
	relay    pipeline.PassthroughRelay
	resolver pipeline.CredentialResolver
	admin    *adminapi.API
	skills   *skillstools.API
	metrics  *pipeline.Metrics
	logger   *slog.Logger
	metricsH http.Handler
	handler  *mcp.StreamableHTTPHandler
}

type route struct {
	appName    string
	management bool
}

// NewRuntimeHandler builds the runtime HTTP surface: Prometheus metrics on
// /metrics, management tools on /mcp, per-app proxied tools on /{app}/mcp,
// and the built-in Phase 7 skills tools on both management and per-app
// routes.
func NewRuntimeHandler(
	table *discovery.Table,
	store *keys.Store,
	renderer *render.Renderer,
	relay pipeline.PassthroughRelay,
	resolver pipeline.CredentialResolver,
	admin *adminapi.API,
	skillClient dynamic.Interface,
	metrics *pipeline.Metrics,
	logger *slog.Logger,
	metricsHandler http.Handler,
) (http.Handler, error) {
	switch {
	case table == nil:
		return nil, fmt.Errorf("new runtime handler: discovery table is required")
	case store == nil:
		return nil, fmt.Errorf("new runtime handler: key store is required")
	case renderer == nil:
		return nil, fmt.Errorf("new runtime handler: renderer is required")
	case admin == nil:
		return nil, fmt.Errorf("new runtime handler: admin api is required")
	case skillClient == nil:
		return nil, fmt.Errorf("new runtime handler: skill client is required")
	}
	if logger == nil {
		logger = slog.Default()
	}

	rh := &runtimeHandler{
		table:    table,
		store:    store,
		renderer: renderer,
		relay:    relay,
		resolver: resolver,
		admin:    admin,
		metrics:  metrics,
		logger:   logger,
		metricsH: metricsHandler,
	}
	skills, err := skillstools.New(skillClient, rh.lookupKey)
	if err != nil {
		return nil, fmt.Errorf("new runtime handler: create skills api: %w", err)
	}
	rh.skills = skills
	rh.handler = mcp.NewStreamableHTTPHandler(rh.serverForRequest, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
	})
	return rh, nil
}

func (h *runtimeHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.URL.Path == metricsPath && h.metricsH != nil {
		h.metricsH.ServeHTTP(w, req)
		return
	}

	rt, ok := parseRoute(req.URL.Path)
	if !ok {
		http.NotFound(w, req)
		return
	}
	if !rt.management {
		if _, exists := h.table.Get(rt.appName); !exists {
			http.NotFound(w, req)
			return
		}
	}
	h.handler.ServeHTTP(w, req)
}

// managementInstructions is sent as this session's MCP `instructions`
// field (initialize result) when connecting to mcp2rest's own reserved
// /mcp management path -- the control plane, not a proxied app. Agents
// landing here by mistake (e.g. expecting an app's actual tools) need to
// immediately understand this is the wrong endpoint for that.
const managementInstructions = `You are connected to mcp2rest's MANAGEMENT endpoint (/mcp), not a proxied app.

mcp2rest is a shared REST-to-MCP proxy for homelab apps. This endpoint only
exposes mcp2rest's own control-plane tools (register_app, deregister_app,
list_apps, get_manifest, create_key, list_keys, revoke_key, rotate_key) plus
the built-in Skill documentation tools (list_skills, get_skill, and -- admin
tier only -- create_skill/update_skill/delete_skill). These tools manage
mcp2rest itself and other apps' registrations; they do NOT call any app's
actual REST API.

Most management tools require an admin-tier API key.

To actually call a registered app's real tools (e.g. a download, a query, a
write to that app's own API), connect to that app's OWN endpoint instead:
  https://mcp2rest.<dns-zone>/{app-name}/mcp
(the app-name used is the one shown by list_apps or get_manifest).

Workflow for onboarding a new app's tools into an agent harness:
 1. Call list_apps (admin key) or get_manifest(app) here to see what apps
    exist and their declared tools/schemas.
 2. Configure your MCP client/harness with a NEW server entry pointing at
    https://mcp2rest.<dns-zone>/{app-name}/mcp, authenticated with a
    per-agent-instance key (delivered via that agent's own
    <agent-instance>-mcp2rest-keys Kubernetes Secret, keys.json field).
 3. Reconnect/add that server in your harness -- get_manifest alone does
    not register or add anything automatically; there is no MCP
    self-registration mechanism.`

// appInstructions is sent as this session's MCP `instructions` field when
// connecting to one specific registered app's proxied path
// (/{app-name}/mcp). Keep this generic/app-name-driven -- never special-
// cased to any one app -- since every registered app shares this same
// runtime path.
func appInstructions(appName string) string {
	slug := skillstools.ToolNameSlug(appName)
	listSkillsTool := fmt.Sprintf("list_%s_skills", slug)
	getSkillTool := fmt.Sprintf("get_%s_skill", slug)
	return fmt.Sprintf(`You are connected to mcp2rest's proxy for the %q app (not mcp2rest itself).

mcp2rest is a shared REST-to-MCP proxy: every tool listed in this session's
tools/list is a real call into %q's own REST API, rendered through that
app's Liquid request/response templates -- not a tool belonging to mcp2rest
generically. Tool names, parameters, and behavior are specific to %q; they
do not apply to any other app registered with mcp2rest.

This session also exposes read-only Skill documentation tools named %s and
%s (note: named after %q, not the generic list_skills/get_skill used on
mcp2rest's management endpoint -- do not confuse the two) scoped to %q's
own Skills (plus any cluster-wide Skills not owned by a specific app) --
useful for background/API documentation about %q itself, not for calling
%q's actual tools. Skills belonging to other apps are not visible on this
session; use mcp2rest's management endpoint for cluster-wide Skill
visibility and the generic list_skills/get_skill tool names.

Only tools your API key's tier (user or admin) is authorized for are
listed here; calling an unlisted/unauthorized tool name will be rejected.
Each app has its own tool set and its own endpoint path
(https://mcp2rest.<dns-zone>/{app-name}/mcp) -- tools from other apps are
never available on this session.`, appName, appName, appName, listSkillsTool, getSkillTool, appName, appName, appName, appName)
}

func (h *runtimeHandler) serverForRequest(req *http.Request) *mcp.Server {
	rt, ok := parseRoute(req.URL.Path)
	if !ok {
		return mcp.NewServer(Info, nil)
	}

	apiKey := bearerToken(req)
	if rt.management {
		server := mcp.NewServer(Info, &mcp.ServerOptions{Instructions: managementInstructions})
		h.skills.RegisterReadTools(server, apiKey, "")
		h.skills.RegisterWriteTools(server, apiKey)
		h.admin.RegisterTools(server, apiKey)
		return server
	}

	app, ok := h.table.Get(rt.appName)
	if !ok {
		return mcp.NewServer(Info, nil)
	}
	server := mcp.NewServer(Info, &mcp.ServerOptions{Instructions: appInstructions(app.Name)})
	h.skills.RegisterReadTools(server, apiKey, app.Name)
	server.AddReceivingMiddleware(appToolListFilter(*app, apiKey, h.lookupKey))
	h.registerAppTools(server, *app, apiKey)
	return server
}

func (h *runtimeHandler) registerAppTools(server *mcp.Server, app manifest.App, apiKey string) {
	// We always register every discovered app tool, even ones the current caller
	// is not allowed to use, so a direct tools/call by name still flows through
	// the authz stage and is rejected there. tools/list is filtered separately by
	// appToolListFilter to hide unauthorized app tools from lower-tier callers.
	for _, tool := range app.Tools {
		tool := tool
		mcp.AddTool(server, &mcp.Tool{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: schemaOrEmptyObject(tool.InputSchema),
		}, func(ctx context.Context, _ *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, any, error) {
			callerUsername, toolArgs, err := extractCallerUsername(args)
			if err != nil {
				return nil, nil, fmt.Errorf("tool %q: resolve caller username: %w", tool.Name, err)
			}
			call := &pipeline.CallContext{
				APIKey:         apiKey,
				App:            app,
				ToolName:       tool.Name,
				Args:           toolArgs,
				CallerUsername: callerUsername,
			}
			stages := pipeline.RuntimeStages(h.lookupKey, h.renderer, h.relay, h.resolver, h.metrics, h.logger)
			if err := pipeline.NewExecutor(stages...).Run(ctx, call); err != nil {
				return nil, nil, err
			}
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: call.ResultContent},
				},
			}, nil, nil
		})
	}
}

func appToolListFilter(app manifest.App, apiKey string, lookup pipeline.KeyLookup) mcp.Middleware {
	requiredTiers := make(map[string]manifest.Tier, len(app.Tools))
	for _, tool := range app.Tools {
		requiredTiers[tool.Name] = tool.Tier
	}

	record, authenticated := lookup(apiKey)
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			result, err := next(ctx, method, req)
			if err != nil || method != "tools/list" {
				return result, err
			}

			listResult, ok := result.(*mcp.ListToolsResult)
			if !ok {
				return result, nil
			}

			filtered := listResult.Tools[:0]
			for _, tool := range listResult.Tools {
				requiredTier, appTool := requiredTiers[tool.Name]
				if !appTool {
					filtered = append(filtered, tool)
					continue
				}
				if authenticated && record.Tier.Valid() && record.Tier.Satisfies(requiredTier) {
					filtered = append(filtered, tool)
				}
			}
			listResult.Tools = filtered
			return listResult, nil
		}
	}
}

func (h *runtimeHandler) lookupKey(rawKey string) (pipeline.KeyRecord, bool) {
	record, ok := h.store.Lookup(rawKey)
	if !ok {
		return pipeline.KeyRecord{}, false
	}
	return pipeline.KeyRecord{
		AgentInstance: record.AgentInstance,
		Tier:          record.Tier,
	}, true
}

func parseRoute(path string) (route, bool) {
	switch {
	case path == managementPath:
		return route{management: true}, true
	case strings.HasSuffix(path, managementPath):
		trimmed := strings.TrimSuffix(path, managementPath)
		trimmed = strings.TrimPrefix(trimmed, "/")
		if trimmed == "" || strings.Contains(trimmed, "/") {
			return route{}, false
		}
		return route{appName: trimmed}, true
	default:
		return route{}, false
	}
}

func bearerToken(req *http.Request) string {
	header := strings.TrimSpace(req.Header.Get("Authorization"))
	if header == "" {
		return ""
	}
	if !strings.HasPrefix(strings.ToLower(header), "bearer ") {
		return ""
	}
	return strings.TrimSpace(header[len("Bearer "):])
}

func schemaOrEmptyObject(schema map[string]any) map[string]any {
	if len(schema) == 0 {
		return map[string]any{"type": "object"}
	}

	cloned := make(map[string]any, len(schema)+1)
	for key, value := range schema {
		cloned[key] = value
	}
	if _, ok := cloned["type"]; !ok {
		cloned["type"] = "object"
	}
	return cloned
}

func extractCallerUsername(args map[string]any) (string, map[string]any, error) {
	if len(args) == 0 {
		return "", nil, nil
	}

	cloned := make(map[string]any, len(args))
	for name, value := range args {
		cloned[name] = value
	}

	rawUsername, ok := cloned[callerUsernameArgument]
	if !ok {
		return "", cloned, nil
	}
	delete(cloned, callerUsernameArgument)

	username, ok := rawUsername.(string)
	if !ok {
		return "", nil, fmt.Errorf("%s must be a string, got %T", callerUsernameArgument, rawUsername)
	}
	username = strings.TrimSpace(username)
	if username == "" {
		return "", nil, fmt.Errorf("%s must not be empty", callerUsernameArgument)
	}
	if len(cloned) == 0 {
		cloned = nil
	}

	return username, cloned, nil
}
