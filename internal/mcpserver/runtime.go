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

func (h *runtimeHandler) serverForRequest(req *http.Request) *mcp.Server {
	rt, ok := parseRoute(req.URL.Path)
	server := mcp.NewServer(Info, nil)
	if !ok {
		return server
	}

	apiKey := bearerToken(req)
	if rt.management {
		h.skills.RegisterReadTools(server, apiKey)
		h.skills.RegisterWriteTools(server, apiKey)
		h.admin.RegisterTools(server, apiKey)
		return server
	}

	app, ok := h.table.Get(rt.appName)
	if !ok {
		return server
	}
	h.skills.RegisterReadTools(server, apiKey)
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
