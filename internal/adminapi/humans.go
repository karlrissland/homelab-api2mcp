package adminapi

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/karlrissland/homelab-api2mcp/internal/keys"
	"github.com/karlrissland/homelab-api2mcp/internal/manifest"
)

// HumanKeyAPIVersion is the canonical-human management wire contract version.
const HumanKeyAPIVersion = 1

// PutHumanParams explicitly attests hlctl's canonical identity and eligibility.
type PutHumanParams struct {
	APIVersion       int             `json:"apiVersion"`
	HomelabUserID    string          `json:"homelabUserId"`
	Source           string          `json:"source"`
	SourceRevision   string          `json:"sourceRevision"`
	State            keys.HumanState `json:"state"`
	Admin            bool            `json:"admin"`
	ExpectedRevision int64           `json:"expectedRevision"`
}

// GetHumanParams requests non-secret canonical-human metadata.
type GetHumanParams struct {
	APIVersion    int    `json:"apiVersion"`
	HomelabUserID string `json:"homelabUserId"`
}

// HumanKeyParams pins issuance or retrieval to the attested identity revision.
type HumanKeyParams struct {
	APIVersion       int           `json:"apiVersion"`
	HomelabUserID    string        `json:"homelabUserId"`
	Tier             manifest.Tier `json:"tier"`
	ExpectedRevision int64         `json:"expectedRevision"`
}

// HumanKeyCapabilities advertises source compatibility independently of release tags.
type HumanKeyCapabilities struct {
	APIVersion         int             `json:"apiVersion"`
	PersistenceVersion int             `json:"persistenceVersion"`
	Tiers              []manifest.Tier `json:"tiers"`
	LookupReadOnly     bool            `json:"lookupReadOnly"`
}

// HumanKeyFailure is a safe typed management error, without raw internal diagnostics.
type HumanKeyFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// HumanResult is the versioned object envelope for human tools.
// A success has exactly one payload; a failure has only Error.
type HumanResult struct {
	APIVersion   int                   `json:"apiVersion"`
	Human        *keys.Human           `json:"human,omitempty"`
	Credential   *keys.HumanCredential `json:"credential,omitempty"`
	Capabilities *HumanKeyCapabilities `json:"capabilities,omitempty"`
	Error        *HumanKeyFailure      `json:"error,omitempty"`
}

// SetHumanKeys enables the distinct canonical-human management tools.
func (a *API) SetHumanKeys(humans *keys.HumanKeys) error {
	if humans == nil {
		return &keys.HumanError{Code: "invalid_arguments"}
	}
	a.humans = humans
	return nil
}

func (a *API) requireHumanAdmin(ctx context.Context, apiKey string, version int) error {
	if a.humans == nil {
		return &keys.HumanError{Code: "unsupported_version"}
	}
	// Preserve the existing full-admin authority, but never accept the synthetic
	// debugging identity for human provisioning or protected credential retrieval.
	record, ok := a.store.LookupStored(apiKey)
	if ok && record.HomelabUserID != "" {
		if err := a.humans.Restore(ctx); err != nil {
			return err
		}
		record, ok = a.store.LookupStored(apiKey)
	}
	if !ok || !record.Tier.Satisfies(manifest.TierAdmin) {
		return &keys.HumanError{Code: "unauthorized"}
	}
	if version != HumanKeyAPIVersion {
		return &keys.HumanError{Code: "unsupported_version"}
	}
	return nil
}

// HumanCapabilities reports the authenticated provider's human-key API version.
func (a *API) HumanCapabilities(ctx context.Context, apiKey string) (HumanResult, error) {
	a.humanMu.Lock()
	defer a.humanMu.Unlock()
	if err := a.requireHumanAdmin(ctx, apiKey, HumanKeyAPIVersion); err != nil {
		return HumanResult{}, err
	}
	return HumanResult{APIVersion: HumanKeyAPIVersion, Capabilities: &HumanKeyCapabilities{
		APIVersion: HumanKeyAPIVersion, PersistenceVersion: 1,
		Tiers: []manifest.Tier{manifest.TierUser, manifest.TierAdmin}, LookupReadOnly: true,
	}}, nil
}

// PutHuman explicitly reconciles authoritative eligibility; it never issues keys.
func (a *API) PutHuman(ctx context.Context, apiKey string, params PutHumanParams) (HumanResult, error) {
	a.humanMu.Lock()
	defer a.humanMu.Unlock()
	if err := a.requireHumanAdmin(ctx, apiKey, params.APIVersion); err != nil {
		return HumanResult{}, err
	}
	human, err := a.humans.Put(ctx, keys.Human{
		HomelabUserID: params.HomelabUserID, Source: params.Source, SourceRevision: params.SourceRevision,
		State: params.State, Admin: params.Admin,
	}, params.ExpectedRevision)
	if err != nil {
		return HumanResult{}, err
	}
	return HumanResult{APIVersion: HumanKeyAPIVersion, Human: &human}, nil
}

// GetHuman retrieves protected non-secret identity metadata without issuance.
func (a *API) GetHuman(ctx context.Context, apiKey string, params GetHumanParams) (HumanResult, error) {
	a.humanMu.Lock()
	defer a.humanMu.Unlock()
	if err := a.requireHumanAdmin(ctx, apiKey, params.APIVersion); err != nil {
		return HumanResult{}, err
	}
	human, err := a.humans.Get(ctx, params.HomelabUserID)
	if err != nil {
		return HumanResult{}, err
	}
	return HumanResult{APIVersion: HumanKeyAPIVersion, Human: &human}, nil
}

// CreateHumanKey deliberately issues a missing eligible tier; existing keys fail.
func (a *API) CreateHumanKey(ctx context.Context, apiKey string, params HumanKeyParams) (HumanResult, error) {
	a.humanMu.Lock()
	defer a.humanMu.Unlock()
	if err := a.requireHumanAdmin(ctx, apiKey, params.APIVersion); err != nil {
		return HumanResult{}, err
	}
	credential, err := a.humans.Create(ctx, params.HomelabUserID, params.Tier, params.ExpectedRevision)
	if err != nil {
		return HumanResult{}, err
	}
	return HumanResult{APIVersion: HumanKeyAPIVersion, Credential: &credential}, nil
}

// GetHumanKey returns protected persisted material without minting or rotating.
func (a *API) GetHumanKey(ctx context.Context, apiKey string, params HumanKeyParams) (HumanResult, error) {
	a.humanMu.Lock()
	defer a.humanMu.Unlock()
	if err := a.requireHumanAdmin(ctx, apiKey, params.APIVersion); err != nil {
		return HumanResult{}, err
	}
	credential, err := a.humans.GetKey(ctx, params.HomelabUserID, params.Tier, params.ExpectedRevision)
	if err != nil {
		return HumanResult{}, err
	}
	return HumanResult{APIVersion: HumanKeyAPIVersion, Credential: &credential}, nil
}

func humanResponse(result HumanResult, err error) (*mcp.CallToolResult, HumanResult, error) {
	if err == nil {
		return nil, result, nil
	}
	code := "persistence_failed"
	var failure *keys.HumanError
	if errors.As(err, &failure) {
		code = failure.Code
	}
	result = HumanResult{APIVersion: HumanKeyAPIVersion, Error: &HumanKeyFailure{
		Code: code, Message: "human credentials: " + code,
	}}
	return &mcp.CallToolResult{IsError: true}, result, nil
}

func (a *API) registerHumanTools(server *mcp.Server, apiKey string) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "human_key_capabilities", Description: "Read canonical-human credential API compatibility. Requires a real stored admin gateway key; the debugging auth bypass is insufficient. Does not issue or retrieve credentials.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, HumanResult, error) {
		result, err := a.HumanCapabilities(ctx, apiKey)
		return humanResponse(result, err)
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "put_human", Description: "Explicitly attest hlctl's authoritative canonical homelab_user_id, source revision, active/disabled/deleted state, and admin eligibility. Requires a real stored admin gateway key and expected identity revision (0 only for initial provisioning). Does not issue keys. Disable/deletion revoke both tiers; admin-role loss revokes only admin. Deleted IDs cannot reactivate. Do not derive identity from caller usernames or emails.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, params PutHumanParams) (*mcp.CallToolResult, HumanResult, error) {
		result, err := a.PutHuman(ctx, apiKey, params)
		return humanResponse(result, err)
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "get_human", Description: "Read persisted canonical-human identity and eligibility metadata, including tombstones. Requires a real stored admin gateway key. No issuance, rotation, or persistent writes.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, params GetHumanParams) (*mcp.CallToolResult, HumanResult, error) {
		result, err := a.GetHuman(ctx, apiKey, params)
		return humanResponse(result, err)
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "create_human_key", Description: "Deliberately issue exactly one missing gateway key for an attested canonical human and eligible user/admin tier. Requires a real stored admin gateway key and exact identity revision. Existing keys fail with key_exists; never use this as a fallback after lookup failure. Protect and deliver the raw credential; it is not an agent, bootstrap, or upstream credential.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, params HumanKeyParams) (*mcp.CallToolResult, HumanResult, error) {
		result, err := a.CreateHumanKey(ctx, apiKey, params)
		return humanResponse(result, err)
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "get_human_key", Description: "Retrieve one persisted eligible canonical-human gateway credential at the exact identity revision. Requires a real stored admin gateway key. Read-only: never issues, rotates, repairs, or writes persistent state. Missing/denied/duplicate/disabled/storage failures return typed errors, not substitutes. Protect the returned raw key.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, params HumanKeyParams) (*mcp.CallToolResult, HumanResult, error) {
		result, err := a.GetHumanKey(ctx, apiKey, params)
		return humanResponse(result, err)
	})
}
