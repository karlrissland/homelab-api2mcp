package pipeline

import (
	"context"
	"fmt"

	"github.com/karlrissland/homelab-api2mcp/internal/manifest"
)

const (
	// DevUserAPIKey is the Phase 3 hardcoded user-tier development key.
	DevUserAPIKey = "phase3-user-key"
	// DevAdminAPIKey is the Phase 3 hardcoded admin-tier development key.
	DevAdminAPIKey = "phase3-admin-key"
)

// NewAuthenticationStage builds the authn stage.
//
// A nil key map means "use the Phase 3 hardcoded development keys". An
// empty, non-nil map means "accept no keys".
func NewAuthenticationStage(keys map[string]KeyRecord) Stage {
	return NewStage(authnStageName, authenticationHandler(keys))
}

// DevKeys returns the Phase 3 hardcoded development keys.
func DevKeys() map[string]KeyRecord {
	return map[string]KeyRecord{
		DevUserAPIKey: {
			AgentInstance: "phase3-user-agent",
			Tier:          manifest.TierUser,
		},
		DevAdminAPIKey: {
			AgentInstance: "phase3-admin-agent",
			Tier:          manifest.TierAdmin,
		},
	}
}

func authenticationHandler(keys map[string]KeyRecord) StageFunc {
	resolvedKeys := cloneKeys(keys)
	return func(_ context.Context, call *CallContext) error {
		if call.APIKey == "" {
			return ErrMissingAPIKey
		}

		record, ok := resolvedKeys[call.APIKey]
		if !ok {
			return ErrInvalidAPIKey
		}
		if !record.Tier.Valid() {
			return fmt.Errorf("api key for agent instance %q has invalid tier %q", record.AgentInstance, record.Tier)
		}

		call.Caller = Caller(record)
		return nil
	}
}

func cloneKeys(keys map[string]KeyRecord) map[string]KeyRecord {
	if keys == nil {
		keys = DevKeys()
	}

	cloned := make(map[string]KeyRecord, len(keys))
	for key, record := range keys {
		cloned[key] = record
	}
	return cloned
}
