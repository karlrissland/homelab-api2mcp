// Package render executes Liquid-backed request and response transforms
// for rendered MCP tools.
package render

import "github.com/karlrissland/homelab-api2mcp/internal/manifest"

// Context is the data exposed to Liquid templates for a tool call.
//
// Phase 8 populates Args plus the multi-user caller context
// (`caller.agentInstance`, `caller.username`, `caller.tier`,
// `caller.appInstance`) so manifests can scope upstream requests to one
// end user without leaking the reserved caller username transport field
// into the tool's own args.* object.
type Context struct {
	Args   map[string]any `json:"args"`
	Caller *Caller        `json:"caller,omitempty"`
}

// Caller describes the per-caller identity surface exposed to Liquid
// templates.
type Caller struct {
	AgentInstance string        `json:"agentInstance"`
	Username      string        `json:"username"`
	Tier          manifest.Tier `json:"tier"`
	AppInstance   string        `json:"appInstance"`
}
