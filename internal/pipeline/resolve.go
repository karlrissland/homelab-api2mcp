package pipeline

import "context"

// NewToolResolveStage builds the tool-resolve stage.
func NewToolResolveStage() Stage {
	return NewStage(toolResolveStageName, func(_ context.Context, call *CallContext) error {
		tool, err := resolveTool(call.App, call.ToolName)
		if err != nil {
			return err
		}
		call.Tool = tool
		return nil
	})
}
