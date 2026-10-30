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

// NativeProxy forwards authenticated requests to an app's native MCP endpoint.
type NativeProxy interface {
	Forward(http.ResponseWriter, *http.Request, manifest.App, string)
}

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
	proxy    NativeProxy
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
// and native endpoint passthrough on /{app}/mcp when no tools are declared.
// Built-in skills tools remain on management and locally defined app routes.
func NewRuntimeHandler(
	table *discovery.Table,
	store *keys.Store,
	renderer *render.Renderer,
	relay pipeline.PassthroughRelay,
	proxy NativeProxy,
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
	case proxy == nil:
		return nil, fmt.Errorf("new runtime handler: native proxy is required")
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
		proxy:    proxy,
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
		app, exists := h.table.Get(rt.appName)
		if !exists {
			http.NotFound(w, req)
			return
		}
		if app.IsNativeMCP() {
			record, authenticated := h.lookupKey(bearerToken(req))
			if !authenticated || !record.Tier.Valid() {
				w.Header().Set("WWW-Authenticate", `Bearer realm="mcp2rest"`)
				http.Error(w, "valid gateway API key required", http.StatusUnauthorized)
				return
			}
			var credential string
			if h.resolver != nil && app.UpstreamCredentialEnv != "" {
				credential = h.resolver.Credential(manifest.ToolTypePassthrough, app.Name, app.UpstreamCredentialEnv)
			}
			h.proxy.Forward(w, req, *app, credential)
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

mcp2rest is a shared REST/MCP gateway for homelab apps. This endpoint only
exposes mcp2rest's own control-plane tools (register_app, deregister_app,
list_apps, get_manifest, create_key, list_keys, get_key, revoke_key,
rotate_key, reload_cache) plus the built-in Skill documentation tools
(list_skills, get_skill, create_skill, update_skill, delete_skill).
These tools do NOT call a registered app's actual API.

ALL management calls require an admin-tier gateway API key, including
list_apps and get_manifest. Skill reads accept any valid user/admin key;
Skill writes require admin. These tools are listed even when a caller
cannot use them: discovery is not authorization. There is no separate
proxy-admin scope, and gateway keys are not restricted to individual apps.

Generic list_skills/get_skill read Skills cluster-wide. Use a returned
resource's name and namespace with get_skill; skillName is not its identity.

Workflow for onboarding a new app's tools into an agent harness:
 1. With an admin key, call list_apps({}) or
    get_manifest({"appName":"<registered-name>"}). These return registration
    metadata, not an API key. A native registration has no declared tools;
    discover its tools on the app endpoint instead.
 2. Configure your MCP client/harness with a NEW server entry pointing at
    the returned mcpInternalUrl for in-cluster access. Otherwise combine
    mcpEndpointPath (/{app-name}/mcp) with your operator-supplied gateway
    base URL. Authenticate with Authorization: Bearer <your-agent-key>.
    Authoritative agent keys persist in the gateway namespace; hlctl/the
    provisioner delivers the harness credential or agent-namespace copy.
    Gateway keys are distinct from server-owned upstream credentials.
 3. Initialize the new app connection, call tools/list, and use its exact
    tool names and schemas in tools/call. get_manifest does not add a
    connection or make app tools callable on this management session.

Declared app routes enforce per-tool user/admin tiers and add app/global
Skill reads. Native routes authenticate a valid gateway key but relay the
upstream MCP surface without gateway per-tool tier checks or injected Skill
tools; upstream permissions depend on the server-owned upstream credential.`

// appInstructions is sent as this session's MCP `instructions` field when
// connecting to one registered app's declared-tool path (/{app-name}/mcp).
// Native endpoint routes bypass this server and retain upstream instructions.
func appInstructions(appName string) string {
	slug := skillstools.ToolNameSlug(appName)
	listSkillsTool := fmt.Sprintf("list_%s_skills", slug)
	getSkillTool := fmt.Sprintf("get_%s_skill", slug)
	return fmt.Sprintf(`You are connected to mcp2rest's declared-tool proxy for the %q app (not mcp2rest itself).

This session exposes declared tools for %q, plus gateway Skill read tools.
Rendered tools call %q's REST API through Liquid request/response templates;
individually declared passthrough tools instead call an upstream MCP tool.
Use tools/list and the advertised input schemas rather than guessing names.

This session also exposes read-only Skill documentation tools named %s and
%s (derived from %q, not the generic list_skills/get_skill names used on
the gateway management endpoint). They show Skills labeled
skills.homelab.dev/app=%q plus global Skills with no app label, across
namespaces; another app's Skills are hidden. Use these for documentation
about %q, not to perform app actions. Pass a listed resource's name and
namespace to the get tool. Both Skill tools require a valid gateway key
even though their names remain visible without one.

For %q's actual app actions, only declared tools allowed by your gateway
key's user/admin tier appear in tools/list. Calling a declared admin tool
with a user key is rejected before upstream execution. Gateway keys are
agent-scoped, not app-scoped; the same admin tier also grants management
access on /mcp. No management or Skill write tools are provided here.

Rendered results are text; upstream HTTP 400+ sets isError. Declared MCP
passthrough flattens upstream content to text and does not preserve its
isError flag, so inspect content as well as errors before assuming success.
The optional __mcp2rest_caller_username argument is caller-supplied, not
verified end-user identity; it must be permitted by the tool schema and
is stripped before template/relay arguments are passed upstream.

Connect to a different app's own endpoint to discover its tools.
Whole native MCP endpoint registrations are a different
mode: they retain upstream instructions/tools and do not inject these
gateway Skill tools or enforce declared-tool tier filtering.`, appName, appName, appName, listSkillsTool, getSkillTool, appName, appName, appName, appName)
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
				IsError: call.RenderResult.IsError,
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
