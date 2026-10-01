package pipeline

import (
	"context"
	"log/slog"
	"time"
)

// NewLoggingStage builds the structured stdout logging stage.
func NewLoggingStage(logger *slog.Logger) Stage {
	resolvedLogger := logger
	if resolvedLogger == nil {
		resolvedLogger = slog.Default()
	}

	return NewStage(loggingStageName, func(_ context.Context, call *CallContext) error {
		startedAt := time.Now()
		call.addFinalizer(func(err error) {
			attrs := []any{
				"app", callAppName(call),
				"tool", callToolName(call),
				"caller_agent_instance", call.Caller.AgentInstance,
				"tier", call.Caller.Tier,
				"outcome", outcomeLabel(err),
				"duration_ms", time.Since(startedAt).Milliseconds(),
			}
			if err != nil {
				attrs = append(attrs, "error", err.Error())
				resolvedLogger.Warn("mcp2rest tool call completed", attrs...)
				return
			}
			resolvedLogger.Info("mcp2rest tool call completed", attrs...)
		})
		return nil
	})
}
