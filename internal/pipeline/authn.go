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

// KeyLookup resolves a raw API key to its record.
type KeyLookup func(key string) (KeyRecord, bool)

// NewAuthenticationStage builds the authn stage.
//
// A nil key map means "use the Phase 3 hardcoded development keys". An
// empty, non-nil map means "accept no keys".
func NewAuthenticationStage(keys map[string]KeyRecord) Stage {
	return NewAuthenticationStageFromLookup(cloneLookup(keys))
}

// NewAuthenticationStageFromLookup builds the authn stage from a dynamic
// lookup function, preserving internal/pipeline's decoupling from any
// specific key-store implementation.
func NewAuthenticationStageFromLookup(lookup KeyLookup) Stage {
	return NewStage(authnStageName, authenticationHandler(lookup))
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

func authenticationHandler(lookup KeyLookup) StageFunc {
	resolvedLookup := cloneLookupFn(lookup)
	return func(_ context.Context, call *CallContext) error {
		// The empty-key check is ordered after the lookup (rather than
		// short-circuiting first) so an auth-disabled lookup
		// (keys.Store.DisableAuth) -- which succeeds unconditionally,
		// even for an empty key -- can still authenticate a caller with
		// no key presented at all.
		record, ok := resolvedLookup(call.APIKey)
		if !ok {
			if call.APIKey == "" {
				return ErrMissingAPIKey
			}
			return ErrInvalidAPIKey
		}
		if !record.Tier.Valid() {
			return fmt.Errorf("api key for agent instance %q has invalid tier %q", record.AgentInstance, record.Tier)
		}

		call.Caller = Caller(record)
		return nil
	}
}

func cloneLookup(keys map[string]KeyRecord) KeyLookup {
	cloned := cloneKeys(keys)
	return func(key string) (KeyRecord, bool) {
		record, ok := cloned[key]
		return record, ok
	}
}

func cloneLookupFn(lookup KeyLookup) KeyLookup {
	if lookup == nil {
		return cloneLookup(nil)
	}
	return lookup
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
