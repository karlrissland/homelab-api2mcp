package passthrough

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/karlrissland/homelab-api2mcp/internal/manifest"
)

func TestRelayCallRoundTrip(t *testing.T) {
	t.Parallel()

	upstreamServer := mcp.NewServer(&mcp.Implementation{Name: "upstream", Version: "0.0.1"}, nil)
	mcp.AddTool(upstreamServer, &mcp.Tool{
		Name: "echo_public",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
			},
		},
	}, func(_ context.Context, _ *mcp.CallToolRequest, params struct {
		Name string `json:"name"`
	}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: "hello " + params.Name},
			},
		}, nil, nil
	})

	upstream := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return upstreamServer
	}, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
	}))
	defer upstream.Close()

	relay := New(upstream.Client())
	got, err := relay.Call(context.Background(), manifest.App{
		Name:           "demo",
		UpstreamMCPURL: upstream.URL,
	}, manifest.Tool{
		Name:             "relay_echo",
		Type:             manifest.ToolTypePassthrough,
		UpstreamToolName: "echo_public",
	}, map[string]any{
		"name": "alice",
	}, "")
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if got != "hello alice" {
		t.Fatalf("Call() = %q, want %q", got, "hello alice")
	}
}

func TestRelayCallInjectsBearerCredential(t *testing.T) {
	t.Parallel()

	var gotAuth string
	upstreamServer := mcp.NewServer(&mcp.Implementation{Name: "upstream", Version: "0.0.1"}, nil)
	mcp.AddTool(upstreamServer, &mcp.Tool{
		Name: "whoami",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "ok"}},
		}, nil, nil
	})

	mux := http.NewServeMux()
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return upstreamServer
	}, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		handler.ServeHTTP(w, r)
	})
	upstream := httptest.NewServer(mux)
	defer upstream.Close()

	relay := New(upstream.Client())
	_, err := relay.Call(context.Background(), manifest.App{
		Name:           "demo",
		UpstreamMCPURL: upstream.URL,
	}, manifest.Tool{
		Name:             "relay_whoami",
		Type:             manifest.ToolTypePassthrough,
		UpstreamToolName: "whoami",
	}, map[string]any{}, "s3cr3t")
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if want := "Bearer s3cr3t"; gotAuth != want {
		t.Fatalf("Authorization header = %q, want %q", gotAuth, want)
	}
}
