package pipeline

import (
	"context"
	"fmt"

	"github.com/karlrissland/homelab-api2mcp/internal/manifest"
	"github.com/karlrissland/homelab-api2mcp/internal/render"
)

// ToolRenderer executes rendered tools against their upstream.
type ToolRenderer interface {
	Execute(ctx context.Context, app manifest.App, tool manifest.Tool, data render.Context) (render.Result, error)
}

// NewRenderRequestStage builds the render-request stage.
func NewRenderRequestStage() Stage {
	return NewStage(renderRequestStageName, func(_ context.Context, call *CallContext) error {
		if !call.Caller.Tier.Valid() {
			return ErrUnauthenticated
		}
		if call.Tool.Name == "" {
			return ErrToolNotFound
		}
		call.RenderContext = render.Context{
			Args: call.Args,
			Caller: &render.Caller{
				AgentInstance: call.Caller.AgentInstance,
				Username:      call.CallerUsername,
				Tier:          call.Caller.Tier,
				AppInstance:   call.App.Name,
			},
		}
		return nil
	})
}

// NewUpstreamCallStage builds the upstream-call stage.
func NewUpstreamCallStage(renderer ToolRenderer) Stage {
	return NewStage(upstreamCallStageName, func(ctx context.Context, call *CallContext) error {
		if renderer == nil {
			return fmt.Errorf("upstream call stage: renderer is required")
		}
		switch call.Tool.Type {
		case manifest.ToolTypeRendered:
			result, err := renderer.Execute(ctx, call.App, call.Tool, call.RenderContext)
			if err != nil {
				return err
			}
			call.RenderResult = result
			return nil
		case manifest.ToolTypePassthrough:
			return fmt.Errorf("%w: tool %q on app %q", ErrPassthroughNotSupported, call.Tool.Name, call.App.Name)
		default:
			return fmt.Errorf("tool %q: unsupported type %q", call.Tool.Name, call.Tool.Type)
		}
	})
}

// NewRenderResponseStage builds the render-response stage.
func NewRenderResponseStage() Stage {
	return NewStage(renderResponseStageName, func(_ context.Context, call *CallContext) error {
		call.ResultContent = call.RenderResult.Content
		return nil
	})
}

// NewRespondStage builds the respond stage.
func NewRespondStage() Stage {
	return NewStage(respondStageName, func(_ context.Context, call *CallContext) error {
		if call.ResultContent == "" && call.RenderResult.Content != "" {
			call.ResultContent = call.RenderResult.Content
		}
		return nil
	})
}
