// Package render executes Liquid-backed request and response transforms
// for rendered MCP tools.
package render

import "github.com/karlrissland/homelab-api2mcp/internal/manifest"

// Context is the data exposed to a tool's Liquid request template.
//
// The pipeline populates Args, Credential, and Caller. The caller username is
// supplied by the agent, not verified end-user identity, and its reserved
// transport argument is stripped from Args. Response templates receive
// request/response bindings instead of this context.
type Context struct {
	Args   map[string]any `json:"args"`
	Caller *Caller        `json:"caller,omitempty"`
	// Credential is the resolved upstream credential value for this
	// tool's EffectiveCredentialEnv, looked up from the in-memory
	// upstream-credential cache. It is empty when the tool declares no
	// credential, or when one is declared but not yet provisioned
	// (fail-open — the empty value is passed through so the backend's
	// own auth rejection becomes the caller-visible error).
	Credential string `json:"credential,omitempty"`
}

// Caller describes the per-caller identity surface exposed to Liquid
// templates.
type Caller struct {
	AgentInstance string        `json:"agentInstance"`
	Username      string        `json:"username"`
	Tier          manifest.Tier `json:"tier"`
	AppInstance   string        `json:"appInstance"`
}
