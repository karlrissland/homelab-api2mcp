package pipeline

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/karlrissland/homelab-api2mcp/internal/manifest"
)

func TestAuthenticationStageAllowsEmptyKeyWhenLookupIsAlwaysOK(t *testing.T) {
	t.Parallel()

	// Mirrors keys.Store.DisableAuth()'s behavior: Lookup succeeds
	// unconditionally, even for an empty key, so the authn stage must not
	// short-circuit on an empty APIKey before consulting the lookup.
	alwaysOK := func(string) (KeyRecord, bool) {
		return KeyRecord{AgentInstance: "auth-disabled", Tier: manifest.TierAdmin}, true
	}

	call := &CallContext{APIKey: ""}
	stage := NewAuthenticationStageFromLookup(alwaysOK)
	if err := stage.Handle(context.Background(), call); err != nil {
		t.Fatalf("Handle() error = %v, want nil", err)
	}
	if call.Caller.Tier != manifest.TierAdmin {
		t.Fatalf("Caller.Tier = %q, want %q", call.Caller.Tier, manifest.TierAdmin)
	}
}

func TestDefaultStagesOrder(t *testing.T) {
	stages := DefaultStages(nil)

	got := make([]string, 0, len(stages))
	for _, stage := range stages {
		got = append(got, stage.Name())
	}

	want := []string{
		authnStageName,
		authzStageName,
		toolResolveStageName,
		renderRequestStageName,
		upstreamCallStageName,
		renderResponseStageName,
		respondStageName,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("DefaultStages() = %v, want %v", got, want)
	}
}

func TestExecutorRunPhase3Matrix(t *testing.T) {
	t.Parallel()

	app := manifest.App{
		Name: "demo-app",
		Tools: []manifest.Tool{
			{
				Name:             "list_repos",
				Tier:             manifest.TierUser,
				Type:             manifest.ToolTypeRendered,
				RequestTemplate:  "request",
				ResponseTemplate: "response",
			},
			{
				Name:             "create_user",
				Tier:             manifest.TierAdmin,
				Type:             manifest.ToolTypeRendered,
				RequestTemplate:  "request",
				ResponseTemplate: "response",
			},
		},
	}

	keys := map[string]KeyRecord{
		"user-key": {
			AgentInstance: "user-agent",
			Tier:          manifest.TierUser,
		},
		"admin-key": {
			AgentInstance: "admin-agent",
			Tier:          manifest.TierAdmin,
		},
	}

	testCases := []struct {
		name             string
		apiKey           string
		toolName         string
		wantErr          error
		wantStage        string
		wantCaller       Caller
		wantResolvedTool string
		wantAuthzCalls   int
		wantResolveCalls int
	}{
		{
			name:             "valid user key calling user tool",
			apiKey:           "user-key",
			toolName:         "list_repos",
			wantCaller:       Caller{AgentInstance: "user-agent", Tier: manifest.TierUser},
			wantResolvedTool: "list_repos",
			wantAuthzCalls:   1,
			wantResolveCalls: 1,
		},
		{
			name:             "valid user key calling admin tool",
			apiKey:           "user-key",
			toolName:         "create_user",
			wantErr:          ErrTierForbidden,
			wantStage:        authzStageName,
			wantCaller:       Caller{AgentInstance: "user-agent", Tier: manifest.TierUser},
			wantAuthzCalls:   1,
			wantResolveCalls: 0,
		},
		{
			name:             "valid admin key calling user tool",
			apiKey:           "admin-key",
			toolName:         "list_repos",
			wantCaller:       Caller{AgentInstance: "admin-agent", Tier: manifest.TierAdmin},
			wantResolvedTool: "list_repos",
			wantAuthzCalls:   1,
			wantResolveCalls: 1,
		},
		{
			name:             "valid admin key calling admin tool",
			apiKey:           "admin-key",
			toolName:         "create_user",
			wantCaller:       Caller{AgentInstance: "admin-agent", Tier: manifest.TierAdmin},
			wantResolvedTool: "create_user",
			wantAuthzCalls:   1,
			wantResolveCalls: 1,
		},
		{
			name:             "invalid key is rejected before later stages",
			apiKey:           "bogus-key",
			toolName:         "list_repos",
			wantErr:          ErrInvalidAPIKey,
			wantStage:        authnStageName,
			wantAuthzCalls:   0,
			wantResolveCalls: 0,
		},
		{
			name:             "missing key is rejected before later stages",
			toolName:         "list_repos",
			wantErr:          ErrMissingAPIKey,
			wantStage:        authnStageName,
			wantAuthzCalls:   0,
			wantResolveCalls: 0,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			authzCalls := 0
			resolveCalls := 0
			stages := DefaultStages(keys)
			stages[1] = countingStage{Stage: stages[1], count: &authzCalls}
			stages[2] = countingStage{Stage: stages[2], count: &resolveCalls}

			call := &CallContext{
				APIKey:   tc.apiKey,
				App:      app,
				ToolName: tc.toolName,
			}

			err := NewExecutor(stages...).Run(context.Background(), call)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Run() unexpected error: %v", err)
				}
				if call.Caller != tc.wantCaller {
					t.Fatalf("CallContext.Caller = %+v, want %+v", call.Caller, tc.wantCaller)
				}
				if call.Tool.Name != tc.wantResolvedTool {
					t.Fatalf("CallContext.Tool.Name = %q, want %q", call.Tool.Name, tc.wantResolvedTool)
				}
			} else {
				if err == nil {
					t.Fatal("Run() error = nil, want non-nil")
				}
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Run() error = %v, want errors.Is(..., %v)", err, tc.wantErr)
				}

				var stageErr *StageError
				if !errors.As(err, &stageErr) {
					t.Fatalf("Run() error = %T, want *StageError", err)
				}
				if stageErr.Stage != tc.wantStage {
					t.Fatalf("StageError.Stage = %q, want %q", stageErr.Stage, tc.wantStage)
				}
				if call.Caller != tc.wantCaller {
					t.Fatalf("CallContext.Caller = %+v, want %+v", call.Caller, tc.wantCaller)
				}
				if tc.wantResolvedTool == "" && call.Tool.Name != "" {
					t.Fatalf("CallContext.Tool.Name = %q, want unresolved tool", call.Tool.Name)
				}
			}

			if authzCalls != tc.wantAuthzCalls {
				t.Fatalf("authz calls = %d, want %d", authzCalls, tc.wantAuthzCalls)
			}
			if resolveCalls != tc.wantResolveCalls {
				t.Fatalf("tool-resolve calls = %d, want %d", resolveCalls, tc.wantResolveCalls)
			}
		})
	}
}

func TestNewAuthenticationStageFromLookup(t *testing.T) {
	t.Parallel()

	stage := NewAuthenticationStageFromLookup(func(key string) (KeyRecord, bool) {
		if key != "dynamic-key" {
			return KeyRecord{}, false
		}
		return KeyRecord{AgentInstance: "dynamic-agent", Tier: manifest.TierAdmin}, true
	})

	call := &CallContext{APIKey: "dynamic-key"}
	if err := stage.Handle(context.Background(), call); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if call.Caller.AgentInstance != "dynamic-agent" || call.Caller.Tier != manifest.TierAdmin {
		t.Fatalf("CallContext.Caller = %+v, want dynamic-agent/admin", call.Caller)
	}
}

type countingStage struct {
	Stage
	count *int
}

func (s countingStage) Handle(ctx context.Context, call *CallContext) error {
	*s.count = *s.count + 1
	return s.Stage.Handle(ctx, call)
}
