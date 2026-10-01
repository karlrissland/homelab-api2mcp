package mcpserver

import (
	"context"
	"fmt"
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

type runtimeHandler struct {
	table    *discovery.Table
	store    *keys.Store
	renderer *render.Renderer
	admin    *adminapi.API
	skills   *skillstools.API
	handler  *mcp.StreamableHTTPHandler
}

type route struct {
	appName    string
	management bool
}

// NewRuntimeHandler builds the runtime HTTP surface: management tools on
// /mcp, per-app proxied tools on /{app}/mcp, and the built-in Phase 7
// skills tools on both management and per-app routes.
func NewRuntimeHandler(table *discovery.Table, store *keys.Store, renderer *render.Renderer, admin *adminapi.API, skillClient dynamic.Interface) (http.Handler, error) {
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

	rh := &runtimeHandler{
		table:    table,
		store:    store,
		renderer: renderer,
		admin:    admin,
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
	h.registerAppTools(server, *app, apiKey)
	return server
}

func (h *runtimeHandler) registerAppTools(server *mcp.Server, app manifest.App, apiKey string) {
	for _, tool := range app.Tools {
		tool := tool
		mcp.AddTool(server, &mcp.Tool{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: schemaOrEmptyObject(tool.InputSchema),
		}, func(ctx context.Context, _ *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, any, error) {
			call := &pipeline.CallContext{
				APIKey:   apiKey,
				App:      app,
				ToolName: tool.Name,
				Args:     args,
			}
			stages := pipeline.RuntimeStages(h.lookupKey, h.renderer)
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
	if schema != nil {
		return schema
	}
	return map[string]any{"type": "object"}
}
