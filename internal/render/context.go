// Package render executes Liquid-backed request and response transforms
// for rendered MCP tools.
package render

import "github.com/karlrissland/homelab-api2mcp/internal/manifest"

// Context is the data exposed to Liquid templates for a tool call.
//
// Phase 2 only populates Args. Caller is reserved for the Phase 8
// multi-user impersonation context (`caller.agentInstance`,
// `caller.username`, `caller.tier`, `caller.appInstance`) so later phases
// can extend the template surface without rewriting this package.
type Context struct {
	Args   map[string]any `json:"args"`
	Caller *Caller        `json:"caller,omitempty"`
}

// Caller describes the future per-caller identity surface exposed to
// Liquid templates. Phase 2 leaves it unset.
type Caller struct {
	AgentInstance string        `json:"agentInstance"`
	Username      string        `json:"username"`
	Tier          manifest.Tier `json:"tier"`
	AppInstance   string        `json:"appInstance"`
}
