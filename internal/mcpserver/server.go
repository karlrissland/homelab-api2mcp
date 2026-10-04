// Package mcpserver builds the mcp2rest MCP (Model Context Protocol)
// server and its Streamable HTTP transport handler.
//
// This package owns only transport/wiring concerns. Tool business logic
// (rendering, authz, upstream calls) is added by later phases via
// internal/pipeline; Phase 1 registers a single hardcoded "echo" tool to
// prove the transport end to end before anything else is layered on.
package mcpserver

import (
	"context"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Info identifies this server to connecting MCP clients.
//
// Description is deliberately NOT set here: mcp.Implementation.Description
// does not exist on the go-sdk version this repo pins (see the version
// pin note on the go-sdk require line in go.mod) -- the equivalent
// explanatory text instead lives entirely in the session Instructions
// string passed to mcp.NewServer, which every client already receives.
var Info = &mcp.Implementation{
	Name:    "mcp2rest",
	Version: "0.1.0",
}

// New builds an MCP server with the echo tool registered.
func New() *mcp.Server {
	server := mcp.NewServer(Info, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "echo",
		Description: "Echoes back the provided message. Proves the MCP transport is wired correctly.",
	}, echo)
	return server
}

// NewHandler wraps server as a Streamable HTTP handler suitable for
// http.ListenAndServe. Every request is served by the same server
// instance; per-request routing to different app instances is added in
// a later phase (internal/discovery).
func NewHandler(server *mcp.Server) http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, nil)
}

// EchoParams is the input schema for the echo tool.
type EchoParams struct {
	Message string `json:"message" jsonschema:"Message to echo back"`
}

func echo(_ context.Context, _ *mcp.CallToolRequest, params *EchoParams) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: params.Message},
		},
	}, nil, nil
}
