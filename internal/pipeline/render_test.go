package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/karlrissland/homelab-api2mcp/internal/manifest"
	"github.com/karlrissland/homelab-api2mcp/internal/render"
)

func TestRuntimeStagesRenderedTool(t *testing.T) {
	t.Parallel()

	renderer := &stubRenderer{
		result: render.Result{Content: "rendered content"},
	}
	call := &CallContext{
		APIKey: "dynamic-key",
		App: manifest.App{
			Name: "demo",
			Tools: []manifest.Tool{
				{
					Name:             "list_repos",
					Tier:             manifest.TierUser,
					Type:             manifest.ToolTypeRendered,
					RequestTemplate:  "{}",
					ResponseTemplate: "{}",
				},
			},
		},
		ToolName:       "list_repos",
		Args:           map[string]any{"page": 2},
		CallerUsername: "alice",
	}

	err := NewExecutor(RuntimeStages(func(key string) (KeyRecord, bool) {
		if key != "dynamic-key" {
			return KeyRecord{}, false
		}
		return KeyRecord{AgentInstance: "agent-a", Tier: manifest.TierUser}, true
	}, renderer, nil, nil, nil)...).Run(context.Background(), call)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if renderer.called != 1 {
		t.Fatalf("renderer called %d times, want 1", renderer.called)
	}
	if call.ResultContent != "rendered content" {
		t.Fatalf("CallContext.ResultContent = %q, want %q", call.ResultContent, "rendered content")
	}
	if call.RenderContext.Caller == nil || call.RenderContext.Caller.AgentInstance != "agent-a" {
		t.Fatalf("CallContext.RenderContext.Caller = %+v, want agent-a caller", call.RenderContext.Caller)
	}
	if call.RenderContext.Caller.Username != "alice" {
		t.Fatalf("CallContext.RenderContext.Caller.Username = %q, want %q", call.RenderContext.Caller.Username, "alice")
	}
}

func TestRuntimeStagesPassthroughTool(t *testing.T) {
	t.Parallel()

	relay := &stubPassthroughRelay{content: "relayed content"}
	call := &CallContext{
		APIKey: "dynamic-key",
		App: manifest.App{
			Name: "demo",
			Tools: []manifest.Tool{
				{
					Name:             "relay",
					Tier:             manifest.TierUser,
					Type:             manifest.ToolTypePassthrough,
					UpstreamToolName: "relay",
				},
			},
			UpstreamMCPURL: "https://upstream.example.invalid/mcp",
		},
		ToolName: "relay",
		Args:     map[string]any{"message": "hi"},
	}

	err := NewExecutor(RuntimeStages(func(string) (KeyRecord, bool) {
		return KeyRecord{AgentInstance: "agent-a", Tier: manifest.TierUser}, true
	}, &stubRenderer{}, relay, nil, nil)...).Run(context.Background(), call)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if relay.called != 1 {
		t.Fatalf("relay called %d times, want 1", relay.called)
	}
	if call.ResultContent != "relayed content" {
		t.Fatalf("CallContext.ResultContent = %q, want %q", call.ResultContent, "relayed content")
	}
}

func TestRuntimeStagesPassthroughRejectedWithoutRelay(t *testing.T) {
	t.Parallel()

	call := &CallContext{
		APIKey: "dynamic-key",
		App: manifest.App{
			Name: "demo",
			Tools: []manifest.Tool{
				{
					Name:             "relay",
					Tier:             manifest.TierUser,
					Type:             manifest.ToolTypePassthrough,
					UpstreamToolName: "relay",
				},
			},
			UpstreamMCPURL: "https://upstream.example.invalid/mcp",
		},
		ToolName: "relay",
	}

	err := NewExecutor(RuntimeStages(func(key string) (KeyRecord, bool) {
		return KeyRecord{AgentInstance: "agent-a", Tier: manifest.TierUser}, true
	}, &stubRenderer{}, nil, nil, nil)...).Run(context.Background(), call)
	if !errors.Is(err, ErrPassthroughNotSupported) {
		t.Fatalf("Run() error = %v, want ErrPassthroughNotSupported", err)
	}
}

type stubRenderer struct {
	result render.Result
	err    error
	called int
}

func (s *stubRenderer) Execute(_ context.Context, _ manifest.App, _ manifest.Tool, _ render.Context) (render.Result, error) {
	s.called++
	return s.result, s.err
}

type stubPassthroughRelay struct {
	content string
	err     error
	called  int
}

func (s *stubPassthroughRelay) Call(_ context.Context, _ manifest.App, _ manifest.Tool, _ map[string]any) (string, error) {
	s.called++
	return s.content, s.err
}
