package pipeline

import (
	"context"
	"fmt"
)

// NewAuthorizationStage builds the authz stage.
//
// It enforces the declared tool's user/admin tier, not a per-key app allowlist.
// Management authorization is handled separately by internal/adminapi.
func NewAuthorizationStage() Stage {
	return NewStage(authzStageName, func(_ context.Context, call *CallContext) error {
		if !call.Caller.Tier.Valid() {
			return ErrUnauthenticated
		}

		tool, err := resolveTool(call.App, call.ToolName)
		if err != nil {
			return err
		}
		if !call.Caller.Tier.Satisfies(tool.Tier) {
			return fmt.Errorf(
				"%w: caller tier %q cannot call tool %q requiring tier %q",
				ErrTierForbidden,
				call.Caller.Tier,
				tool.Name,
				tool.Tier,
			)
		}

		return nil
	})
}
