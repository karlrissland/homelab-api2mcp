// Package pipeline defines the ordered request-stage chain that sits in
// front of every proxied MCP tool call.
//
// Phase 3 implements only authn, authz, and tool resolution for real.
// The render/call/respond stages are deliberate no-op seams for a later
// integration pass that will wire in internal/render and the real
// upstream execution flow.
package pipeline

import (
	"context"
	"errors"
	"fmt"

	"github.com/karlrissland/homelab-api2mcp/internal/manifest"
)

const (
	authnStageName          = "authn"
	authzStageName          = "authz"
	toolResolveStageName    = "tool-resolve"
	renderRequestStageName  = "render-request"
	upstreamCallStageName   = "upstream-call"
	renderResponseStageName = "render-response"
	respondStageName        = "respond"
)

var (
	// ErrMissingAPIKey reports that a call did not include an API key.
	ErrMissingAPIKey = errors.New("missing API key")
	// ErrInvalidAPIKey reports that a call used an unknown API key.
	ErrInvalidAPIKey = errors.New("invalid API key")
	// ErrUnauthenticated reports that a later stage ran before authn
	// populated the caller identity.
	ErrUnauthenticated = errors.New("caller not authenticated")
	// ErrToolNotFound reports that the requested tool does not exist on
	// the target app.
	ErrToolNotFound = errors.New("tool not found")
	// ErrTierForbidden reports that the caller's tier does not satisfy the
	// requested tool's required tier.
	ErrTierForbidden = errors.New("caller tier does not satisfy tool tier")
	// ErrNilCallContext reports misuse of the executor API.
	ErrNilCallContext = errors.New("nil call context")
)

// CallContext carries per-request state as it moves through the pipeline.
//
// The shape stays intentionally simple so a future stage can be backed by
// a sandboxed WASM module without redesigning the execution contract.
type CallContext struct {
	APIKey   string
	App      manifest.App
	ToolName string

	Caller Caller
	Tool   manifest.Tool
}

// Caller is the authenticated identity for the current request.
type Caller struct {
	AgentInstance string
	Tier          manifest.Tier
}

// KeyRecord describes the caller identity bound to one API key.
type KeyRecord struct {
	AgentInstance string
	Tier          manifest.Tier
}

// Stage is one ordered unit of request handling in the pipeline.
type Stage interface {
	Name() string
	Handle(ctx context.Context, call *CallContext) error
}

// StageFunc adapts a function into a Stage.
type StageFunc func(ctx context.Context, call *CallContext) error

type namedStage struct {
	name string
	fn   StageFunc
}

// NewStage builds a named stage from a function.
func NewStage(name string, fn StageFunc) Stage {
	return namedStage{name: name, fn: fn}
}

func (s namedStage) Name() string {
	return s.name
}

func (s namedStage) Handle(ctx context.Context, call *CallContext) error {
	return s.fn(ctx, call)
}

// PlaceholderStage returns a no-op stage used as a seam until the real
// render/upstream/response work is wired in by a later phase.
func PlaceholderStage(name string) Stage {
	return NewStage(name, func(context.Context, *CallContext) error {
		return nil
	})
}

// StageError wraps a stage failure with the stage name so callers can
// tell exactly where the pipeline stopped.
type StageError struct {
	Stage string
	Err   error
}

func (e *StageError) Error() string {
	return fmt.Sprintf("stage %q: %v", e.Stage, e.Err)
}

// Unwrap returns the underlying stage error.
func (e *StageError) Unwrap() error {
	return e.Err
}

// Executor runs a call through an ordered list of stages.
type Executor struct {
	stages []Stage
}

// NewExecutor builds an executor from the provided ordered stage list.
func NewExecutor(stages ...Stage) *Executor {
	cloned := make([]Stage, len(stages))
	copy(cloned, stages)
	return &Executor{stages: cloned}
}

// Run executes each stage in order and stops at the first error.
func (e *Executor) Run(ctx context.Context, call *CallContext) error {
	if call == nil {
		return ErrNilCallContext
	}
	for _, stage := range e.stages {
		if err := stage.Handle(ctx, call); err != nil {
			return &StageError{Stage: stage.Name(), Err: err}
		}
	}
	return nil
}

// DefaultStages returns the Phase 3 stage chain in architectural order.
//
// The first three stages do real work today. The final four are explicit
// placeholders that will be replaced by internal/render and real
// upstream-call/response handling in a later integration phase.
func DefaultStages(keys map[string]KeyRecord) []Stage {
	return []Stage{
		NewAuthenticationStage(keys),
		NewAuthorizationStage(),
		NewToolResolveStage(),
		PlaceholderStage(renderRequestStageName),
		PlaceholderStage(upstreamCallStageName),
		PlaceholderStage(renderResponseStageName),
		PlaceholderStage(respondStageName),
	}
}

func resolveTool(app manifest.App, toolName string) (manifest.Tool, error) {
	tool, ok := app.FindTool(toolName)
	if !ok {
		return manifest.Tool{}, fmt.Errorf("%w: app %q does not expose tool %q", ErrToolNotFound, app.Name, toolName)
	}
	return tool, nil
}
