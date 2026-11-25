package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/karlrissland/homelab-api2mcp/internal/keys"
	"github.com/karlrissland/homelab-api2mcp/internal/manifest"
)

func newHumanAPI(t *testing.T) (*API, string) {
	t.Helper()
	api := newTestAPI(t)
	writer, err := keys.NewSecretWriter(api.client)
	if err != nil {
		t.Fatal(err)
	}
	humans, err := keys.NewHumanKeys(writer, "mcp2rest", api.store)
	if err != nil {
		t.Fatal(err)
	}
	if err := api.SetHumanKeys(humans); err != nil {
		t.Fatal(err)
	}
	key, err := api.store.Mint("test-admin", manifest.TierAdmin)
	if err != nil {
		t.Fatal(err)
	}
	return api, key
}

func humanSession(t *testing.T, api *API, key string) *mcp.ClientSession {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "human-test", Version: "dev"}, nil)
	api.RegisterTools(server, key)
	session := connectSession(t, server)
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Error("close test session:", err)
		}
	})
	return session
}

func humanCall(t *testing.T, session *mcp.ClientSession, name string, args any, code string) HumanResult {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s protocol failure: %v", name, err)
	}
	if result.IsError != (code != "") {
		t.Fatalf("%s isError does not match expected outcome", name)
	}
	envelope := decodeStructured[HumanResult](t, result.StructuredContent)
	if envelope.APIVersion != HumanKeyAPIVersion {
		t.Fatalf("%s omitted the API version", name)
	}
	if code != "" {
		if envelope.Error == nil || envelope.Error.Code != code || envelope.Human != nil || envelope.Credential != nil || envelope.Capabilities != nil {
			t.Fatalf("%s did not return an isolated typed failure", name)
		}
	}
	return envelope
}

func TestHumanManagementVersionedContractAndProtectedLookup(t *testing.T) {
	t.Parallel()
	api, adminKey := newHumanAPI(t)
	session := humanSession(t, api, adminKey)
	capabilities := humanCall(t, session, "human_key_capabilities", map[string]any{}, "")
	if capabilities.Capabilities == nil || !capabilities.Capabilities.LookupReadOnly || capabilities.Capabilities.PersistenceVersion != 1 {
		t.Fatal("capability detection omitted the compatible read-only persistence contract")
	}
	params := PutHumanParams{
		APIVersion: 1, HomelabUserID: "canonical-human", Source: "hlctl",
		SourceRevision: "snapshot-1", State: keys.HumanActive, Admin: true,
	}
	put := humanCall(t, session, "put_human", params, "")
	if put.Human == nil || put.Human.Revision != 1 || put.Credential != nil {
		t.Fatal("identity provisioning issued material or omitted its authoritative revision")
	}
	readIdentity := humanCall(t, session, "get_human", GetHumanParams{APIVersion: 1, HomelabUserID: params.HomelabUserID}, "")
	if readIdentity.Human == nil || *readIdentity.Human != *put.Human {
		t.Fatal("metadata lookup changed the attested identity")
	}
	args := HumanKeyParams{APIVersion: 1, HomelabUserID: params.HomelabUserID, Tier: manifest.TierUser, ExpectedRevision: put.Human.Revision}
	humanCall(t, session, "get_human_key", args, "key_not_found")
	created := humanCall(t, session, "create_human_key", args, "")
	if created.Credential == nil || len(created.Credential.Key) != 43 {
		t.Fatal("explicit issuance omitted a compatible credential")
	}
	humanCall(t, session, "create_human_key", args, "key_exists")
	for range 3 {
		got := humanCall(t, session, "get_human_key", args, "")
		if got.Credential == nil || *got.Credential != *created.Credential {
			t.Fatal("versioned lookup did not reuse the identical persisted credential")
		}
	}
	for _, record := range api.ListKeys(context.Background()) {
		if record.AgentInstance == "" {
			t.Fatal("agent metadata API aliased a human as an agent")
		}
	}
	args.Tier = manifest.TierAdmin
	admin := humanCall(t, session, "create_human_key", args, "")
	if admin.Credential == nil || admin.Credential.Key == created.Credential.Key {
		t.Fatal("human tier credentials were not separate")
	}
	humanAdmin := humanSession(t, api, admin.Credential.Key)
	humanCall(t, humanAdmin, "get_human_key", args, "")
	params.ExpectedRevision, params.SourceRevision, params.Admin = put.Human.Revision, "snapshot-2", false
	humanCall(t, session, "put_human", params, "")
	humanCall(t, humanAdmin, "get_human_key", args, "unauthorized")
	args.ExpectedRevision = 2
	humanCall(t, session, "get_human_key", args, "tier_ineligible")
	args.Tier = manifest.TierUser
	retained := humanCall(t, session, "get_human_key", args, "")
	if retained.Credential.Key != created.Credential.Key {
		t.Fatal("role loss rotated the user's lower-tier credential")
	}
	args.APIVersion = 2
	humanCall(t, session, "get_human_key", args, "unsupported_version")
	args.APIVersion, args.HomelabUserID = 1, "missing-human"
	humanCall(t, session, "get_human_key", args, "human_not_found")
}

func TestHumanManagementDeniesUntrustedCallersEvenWithBypass(t *testing.T) {
	t.Parallel()
	for _, bypass := range []bool{false, true} {
		name := "auth enabled"
		if bypass {
			name = "auth bypass"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			api, adminKey := newHumanAPI(t)
			userKey, err := api.store.Mint("user-agent", manifest.TierUser)
			if err != nil {
				t.Fatal(err)
			}
			if bypass {
				api.store.DisableAuth()
			}
			for _, key := range []string{"", "unknown", userKey} {
				session := humanSession(t, api, key)
				for _, tc := range []struct {
					name string
					args any
				}{
					{name: "human_key_capabilities", args: map[string]any{}},
					{name: "put_human", args: PutHumanParams{APIVersion: 1, HomelabUserID: "human", Source: "hlctl", SourceRevision: "snapshot", State: keys.HumanActive, Admin: true}},
					{name: "get_human", args: GetHumanParams{APIVersion: 1, HomelabUserID: "human"}},
					{name: "create_human_key", args: HumanKeyParams{APIVersion: 1, HomelabUserID: "human", Tier: manifest.TierAdmin, ExpectedRevision: 1}},
					{name: "get_human_key", args: HumanKeyParams{APIVersion: 1, HomelabUserID: "human", Tier: manifest.TierUser, ExpectedRevision: 1}},
				} {
					humanCall(t, session, tc.name, tc.args, "unauthorized")
				}
			}
			humanCall(t, humanSession(t, api, adminKey), "human_key_capabilities", map[string]any{}, "")
		})
	}
}

func TestHumanManagementSchemasAndMalformedInputs(t *testing.T) {
	t.Parallel()
	api, key := newHumanAPI(t)
	session := humanSession(t, api, key)
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{"human_key_capabilities": false, "put_human": false, "get_human": false, "create_human_key": false, "get_human_key": false}
	for _, tool := range tools.Tools {
		if _, relevant := names[tool.Name]; !relevant {
			continue
		}
		schema := decodeStructured[map[string]any](t, tool.OutputSchema)
		if schema["type"] != "object" {
			t.Fatal("human tool output was not object-shaped")
		}
		names[tool.Name] = true
	}
	for name, found := range names {
		if !found {
			t.Fatalf("human API tool %s was not advertised", name)
		}
	}
	for _, args := range []map[string]any{
		{"apiVersion": 1, "homelabUserId": "human", "source": "hlctl", "sourceRevision": "snapshot", "state": "active", "expectedRevision": 0},
		{"apiVersion": 1, "homelabUserId": "human", "tier": "admin", "expectedRevision": "not-an-integer"},
	} {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "put_human", Arguments: args})
		if err == nil && !result.IsError {
			t.Fatal("missing/wrongly typed explicit attestation succeeded")
		}
	}
}

func TestHumanResponseNeverReturnsFailureMaterial(t *testing.T) {
	t.Parallel()
	result, envelope, err := humanResponse(HumanResult{Credential: &keys.HumanCredential{Key: "candidate-secret"}},
		errors.New("candidate-secret backend diagnostic"))
	if err != nil || !result.IsError || envelope.Error == nil || envelope.Credential != nil {
		t.Fatal("failure envelope retained candidate material")
	}
	raw, err := json.Marshal(envelope)
	if err != nil || strings.Contains(string(raw), "candidate-secret") {
		t.Fatal("failure response exposed raw diagnostics")
	}
}

func TestHumanManagementSerializesCallerRoleLossWithProtectedAccess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	api, authority := newHumanAPI(t)
	_, err := api.PutHuman(ctx, authority, PutHumanParams{
		APIVersion: 1, HomelabUserID: "human-admin", Source: "hlctl", SourceRevision: "snapshot-1", State: keys.HumanActive, Admin: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := api.CreateHumanKey(ctx, authority, HumanKeyParams{
		APIVersion: 1, HomelabUserID: "human-admin", Tier: manifest.TierAdmin, ExpectedRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = api.PutHuman(ctx, authority, PutHumanParams{
		APIVersion: 1, HomelabUserID: "other-human", Source: "hlctl", SourceRevision: "snapshot-1", State: keys.HumanActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	params := HumanKeyParams{APIVersion: 1, HomelabUserID: "other-human", Tier: manifest.TierUser, ExpectedRevision: 1}
	if _, err := api.CreateHumanKey(ctx, authority, params); err != nil {
		t.Fatal(err)
	}
	client := api.client.(*fake.Clientset)
	entered, release := make(chan struct{}), make(chan struct{})
	client.PrependReactor("update", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		close(entered)
		<-release
		return false, nil, nil
	})
	putDone := make(chan error, 1)
	go func() {
		_, err := api.PutHuman(ctx, authority, PutHumanParams{
			APIVersion: 1, HomelabUserID: "human-admin", Source: "hlctl", SourceRevision: "snapshot-2", State: keys.HumanActive, ExpectedRevision: 1,
		})
		putDone <- err
	}()
	<-entered
	getDone := make(chan error, 1)
	go func() {
		result, err := api.GetHumanKey(ctx, admin.Credential.Key, params)
		if result.Credential != nil {
			err = errors.New("withdrawn authority retrieved another human's credential")
		}
		getDone <- err
	}()
	close(release)
	if err := <-putDone; err != nil {
		t.Fatal(err)
	}
	var failure *keys.HumanError
	if err := <-getDone; !errors.As(err, &failure) || failure.Code != "unauthorized" {
		t.Fatal("concurrent protected retrieval accepted withdrawn administrator authority")
	}
}
