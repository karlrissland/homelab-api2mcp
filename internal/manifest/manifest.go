// Package manifest defines the shared data shapes describing an app's MCP
// surface: its tools, their tiers, and how each tool is executed (rendered
// via Liquid templates against a REST upstream, or passed through to an
// upstream MCP server). These types are the shared vocabulary between
// internal/discovery (which builds them from ConfigMaps), internal/render
// (which executes rendered tools), internal/pipeline (which authorizes
// calls against a tool's tier), and internal/passthrough (which relays
// passthrough tools) — defining them once here avoids each package
// inventing its own incompatible shape.
package manifest

import "fmt"

// Tier is the authorization level required to call a tool, or held by a
// key. Admin implies User: a key with TierAdmin may call User-tier tools
// too (see Tier.Satisfies).
type Tier string

const (
	// TierUser is the default tier for ordinary, non-privileged tools.
	TierUser Tier = "user"
	// TierAdmin is required for privileged, higher-impact tools.
	TierAdmin Tier = "admin"
)

// Valid reports whether t is a recognized tier.
func (t Tier) Valid() bool {
	switch t {
	case TierUser, TierAdmin:
		return true
	default:
		return false
	}
}

// Satisfies reports whether a key holding tier t is authorized to call a
// tool that requires the tier `required`. Admin satisfies both Admin and
// User requirements; User only satisfies User.
func (t Tier) Satisfies(required Tier) bool {
	if t == TierAdmin {
		return true
	}
	return t == required
}

// ToolType distinguishes a Liquid-rendered tool from a passthrough tool
// that relays directly to an upstream MCP server.
type ToolType string

const (
	// ToolTypeRendered executes RequestTemplate/ResponseTemplate against
	// a REST upstream.
	ToolTypeRendered ToolType = "rendered"
	// ToolTypePassthrough relays the call to an upstream MCP server
	// unchanged, skipping the render stages entirely.
	ToolTypePassthrough ToolType = "passthrough"
)

// Tool describes a single MCP tool exposed by an App.
type Tool struct {
	// Name is the MCP tool name, unique within its App.
	Name string `json:"name"`
	// Description is shown to agents via tools/list.
	Description string `json:"description"`
	// Tier is the minimum authorization tier required to call this tool.
	Tier Tier `json:"tier"`
	// Type selects rendered vs. passthrough execution.
	Type ToolType `json:"type"`
	// InputSchema is the JSON Schema for the tool's parameters.
	InputSchema map[string]any `json:"inputSchema,omitempty"`

	// RequestTemplate and ResponseTemplate are Liquid template source,
	// required when Type == ToolTypeRendered.
	RequestTemplate  string `json:"requestTemplate,omitempty"`
	ResponseTemplate string `json:"responseTemplate,omitempty"`

	// UpstreamToolName is the tool name on the upstream MCP server,
	// required when Type == ToolTypePassthrough (may differ from Name).
	UpstreamToolName string `json:"upstreamToolName,omitempty"`

	// UpstreamCredentialEnv optionally overrides App.UpstreamCredentialEnv
	// for this tool only (e.g. a narrower read-only credential for a
	// "list" tool vs. an admin credential for a "delete" tool). Falls
	// back to the app-level value when empty; see EffectiveCredentialEnv.
	UpstreamCredentialEnv string `json:"upstreamCredentialEnv,omitempty"`
	// UpstreamMCPURL optionally overrides App.UpstreamMCPURL for this
	// tool only (an app proxying more than one native MCP server).
	// Falls back to the app-level value when empty; see
	// EffectiveUpstreamMCPURL.
	UpstreamMCPURL string `json:"upstreamMCPURL,omitempty"`
}

// EffectiveCredentialEnv returns t.UpstreamCredentialEnv if set, otherwise
// a.UpstreamCredentialEnv. An empty result means the tool requires no
// upstream credential.
func (t Tool) EffectiveCredentialEnv(a App) string {
	if t.UpstreamCredentialEnv != "" {
		return t.UpstreamCredentialEnv
	}
	return a.UpstreamCredentialEnv
}

// EffectiveUpstreamMCPURL returns t.UpstreamMCPURL if set, otherwise
// a.UpstreamMCPURL.
func (t Tool) EffectiveUpstreamMCPURL(a App) string {
	if t.UpstreamMCPURL != "" {
		return t.UpstreamMCPURL
	}
	return a.UpstreamMCPURL
}

// Validate reports a descriptive error if t is not well-formed.
func (t Tool) Validate() error {
	if t.Name == "" {
		return fmt.Errorf("tool: name is required")
	}
	if !t.Tier.Valid() {
		return fmt.Errorf("tool %q: invalid tier %q", t.Name, t.Tier)
	}
	switch t.Type {
	case ToolTypeRendered:
		if t.RequestTemplate == "" || t.ResponseTemplate == "" {
			return fmt.Errorf("tool %q: rendered tools require requestTemplate and responseTemplate", t.Name)
		}
	case ToolTypePassthrough:
		if t.UpstreamToolName == "" {
			return fmt.Errorf("tool %q: passthrough tools require upstreamToolName", t.Name)
		}
	default:
		return fmt.Errorf("tool %q: invalid type %q", t.Name, t.Type)
	}
	return nil
}

// App describes one registered application instance's MCP surface, as
// sourced from its ConfigMap (see internal/discovery).
type App struct {
	// Name is the app-instance identifier used in the proxy path
	// (/{app-instance}/mcp) and as the routing-table key.
	Name string `json:"name"`
	// Namespace is the Kubernetes namespace owning the source ConfigMap.
	Namespace string `json:"namespace"`
	// Tools is the set of MCP tools this app exposes.
	Tools []Tool `json:"tools"`
	// UpstreamBaseURL is the base URL rendered requests are issued
	// against (rendered tools only).
	UpstreamBaseURL string `json:"upstreamBaseURL,omitempty"`
	// UpstreamCredentialEnv names the credential this app's requests are
	// authenticated with, resolved out-of-band via the existing
	// internal/credentials-equivalent flow (rendered tools only).
	UpstreamCredentialEnv string `json:"upstreamCredentialEnv,omitempty"`
	// UpstreamMCPURL is the upstream MCP server URL to relay to
	// (passthrough tools only).
	UpstreamMCPURL string `json:"upstreamMCPURL,omitempty"`
}

// Validate reports a descriptive error if a is not well-formed.
func (a App) Validate() error {
	if a.Name == "" {
		return fmt.Errorf("app: name is required")
	}
	if len(a.Tools) == 0 {
		return fmt.Errorf("app %q: at least one tool is required", a.Name)
	}
	seen := make(map[string]struct{}, len(a.Tools))
	for _, t := range a.Tools {
		if err := t.Validate(); err != nil {
			return fmt.Errorf("app %q: %w", a.Name, err)
		}
		if _, dup := seen[t.Name]; dup {
			return fmt.Errorf("app %q: duplicate tool name %q", a.Name, t.Name)
		}
		seen[t.Name] = struct{}{}
		if t.Type == ToolTypePassthrough && t.EffectiveUpstreamMCPURL(a) == "" {
			return fmt.Errorf("app %q: tool %q is passthrough but has no upstreamMCPURL (app- or tool-level)", a.Name, t.Name)
		}
	}
	return nil
}

// FindTool returns the named tool and true, or a zero Tool and false.
func (a App) FindTool(name string) (Tool, bool) {
	for _, t := range a.Tools {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}
