package passthrough

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/karlrissland/homelab-api2mcp/internal/manifest"
)

var relayImplementation = &mcp.Implementation{
	Name:    "mcp2rest-passthrough",
	Version: "0.1.0",
}

// Relay calls passthrough tools on upstream MCP servers.
type Relay struct {
	httpClient *http.Client
}

// New builds a Relay. If httpClient is nil, http.DefaultClient is used.
func New(httpClient *http.Client) *Relay {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Relay{httpClient: httpClient}
}

// Call relays tool to app.UpstreamMCPURL and flattens the upstream result into
// the text content contract used by the rest of the mcp2rest pipeline.
func (r *Relay) Call(ctx context.Context, app manifest.App, tool manifest.Tool, args map[string]any) (content string, err error) {
	if r == nil {
		return "", fmt.Errorf("passthrough relay: relay is required")
	}
	if app.UpstreamMCPURL == "" {
		return "", fmt.Errorf("passthrough relay: app %q has no upstreamMCPURL", app.Name)
	}
	if tool.Type != manifest.ToolTypePassthrough {
		return "", fmt.Errorf("passthrough relay: tool %q has unsupported type %q", tool.Name, tool.Type)
	}
	if tool.UpstreamToolName == "" {
		return "", fmt.Errorf("passthrough relay: tool %q has no upstreamToolName", tool.Name)
	}

	client := mcp.NewClient(relayImplementation, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             app.UpstreamMCPURL,
		HTTPClient:           r.httpClient,
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		return "", fmt.Errorf("passthrough relay: connect to upstream MCP server %q for app %q: %w", app.UpstreamMCPURL, app.Name, err)
	}

	defer func() {
		closeErr := session.Close()
		if closeErr != nil {
			closeErr = fmt.Errorf("passthrough relay: close upstream MCP session for app %q: %w", app.Name, closeErr)
			if err == nil {
				err = closeErr
				content = ""
				return
			}
			err = errors.Join(err, closeErr)
		}
	}()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      tool.UpstreamToolName,
		Arguments: args,
	})
	if err != nil {
		return "", fmt.Errorf("passthrough relay: call upstream tool %q for manifest tool %q on app %q: %w", tool.UpstreamToolName, tool.Name, app.Name, err)
	}

	content, err = flattenResultContent(result)
	if err != nil {
		return "", fmt.Errorf("passthrough relay: flatten upstream result for tool %q on app %q: %w", tool.Name, app.Name, err)
	}

	return content, nil
}

func flattenResultContent(result *mcp.CallToolResult) (string, error) {
	if result == nil {
		return "", fmt.Errorf("nil result")
	}

	if len(result.Content) == 0 {
		if result.StructuredContent == nil {
			return "", nil
		}
		encoded, err := json.Marshal(result.StructuredContent)
		if err != nil {
			return "", fmt.Errorf("marshal structured content: %w", err)
		}
		return string(encoded), nil
	}

	parts := make([]string, 0, len(result.Content))
	for _, item := range result.Content {
		switch content := item.(type) {
		case *mcp.TextContent:
			parts = append(parts, content.Text)
		default:
			encoded, err := item.MarshalJSON()
			if err != nil {
				return "", fmt.Errorf("marshal %T content: %w", item, err)
			}
			parts = append(parts, string(encoded))
		}
	}

	return strings.Join(parts, "\n"), nil
}
