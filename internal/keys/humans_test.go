package keys

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/karlrissland/homelab-api2mcp/internal/manifest"
)

func newHumanFixture(t *testing.T) (*HumanKeys, *Store, *fake.Clientset) {
	t.Helper()
	client := fake.NewSimpleClientset()
	writer, err := NewSecretWriter(client)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	humans, err := NewHumanKeys(writer, "gateway", store)
	if err != nil {
		t.Fatal(err)
	}
	if err := humans.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	return humans, store, client
}

func attestedHuman(id string, admin bool) Human {
	return Human{HomelabUserID: id, Source: "hlctl", SourceRevision: "snapshot-1", State: HumanActive, Admin: admin}
}

func requireHumanCode(t *testing.T, err error, code string) {
	t.Helper()
	var failure *HumanError
	if !errors.As(err, &failure) || failure.Code != code {
		t.Fatalf("failure = %v, want safe code %s", err, code)
	}
}

func TestHumanKeysRequireConfirmedPersistenceAndVerifierAgreement(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	humans, store, client := newHumanFixture(t)
	if _, err := humans.Put(ctx, attestedHuman("human", true), 0); err != nil {
		t.Fatal(err)
	}
	user, err := humans.Create(ctx, "human", manifest.TierUser, 1)
	if err != nil {
		t.Fatal(err)
	}
	client.PrependReactor("update", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		// A success-shaped response is not proof the authoritative state changed.
		return true, action.(k8stesting.UpdateAction).GetObject(), nil
	})
	result, err := humans.Create(ctx, "human", manifest.TierAdmin, 1)
	requireHumanCode(t, err, "persistence_failed")
	if result.Key != "" {
		t.Fatal("unconfirmed persistence returned candidate material")
	}
	if _, ok := store.LookupStored(user.Key); ok {
		t.Fatal("uncertain persistence retained human verification")
	}
	client.ReactionChain = client.ReactionChain[1:]
	if err := humans.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.LookupStored(user.Key); !ok {
		t.Fatal("authoritative recovery did not restore the unchanged credential")
	}
	_, err = humans.GetKey(ctx, "human", manifest.TierAdmin, 1)
	requireHumanCode(t, err, "key_not_found")
}

func TestHumanKeysExplicitIssuanceRestartAndAgentIsolation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	humans, store, client := newHumanFixture(t)
	writer, err := NewSecretWriter(client)
	if err != nil {
		t.Fatal(err)
	}
	agents := testAgentRepository(t, writer, store)
	agentKey, err := agents.Create(ctx, "hermes", "agents", manifest.TierAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.EnsureBootstrap(ctx, "gateway", store); err != nil {
		t.Fatal(err)
	}
	_, err = humans.GetKey(ctx, "human-a", manifest.TierUser, 1)
	requireHumanCode(t, err, "human_not_found")
	identity, err := humans.Put(ctx, attestedHuman("human-a", true), 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = humans.GetKey(ctx, "human-a", manifest.TierUser, identity.Revision)
	requireHumanCode(t, err, "key_not_found")
	user, err := humans.Create(ctx, "human-a", manifest.TierUser, identity.Revision)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := humans.Create(ctx, "human-a", manifest.TierAdmin, identity.Revision)
	if err != nil {
		t.Fatal(err)
	}
	other, err := humans.Put(ctx, attestedHuman("human-b", false), 0)
	if err != nil {
		t.Fatal(err)
	}
	otherKey, err := humans.Create(ctx, "human-b", manifest.TierUser, other.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if user.Key == admin.Key || user.Key == otherKey.Key || admin.Key == otherKey.Key {
		t.Fatal("distinct humans/tiers shared credentials")
	}
	_, err = humans.Create(ctx, "human-a", manifest.TierUser, identity.Revision)
	requireHumanCode(t, err, "key_exists")
	before := make(map[Record]bool)
	for _, record := range store.List() {
		if record.AgentInstance != bootstrapAdminAgent {
			before[record] = true
		}
	}
	restarted := NewStore()
	if _, err := writer.EnsureBootstrap(ctx, "gateway", restarted); err != nil {
		t.Fatal(err)
	}
	if err := testAgentRepository(t, writer, restarted).Restore(ctx); err != nil {
		t.Fatal(err)
	}
	humans, err = NewHumanKeys(writer, "gateway", restarted)
	if err != nil {
		t.Fatal(err)
	}
	if err := humans.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	for _, record := range restarted.List() {
		if record.AgentInstance != bootstrapAdminAgent {
			if !before[record] {
				t.Fatal("restart changed verification identities or creation metadata")
			}
			delete(before, record)
		}
	}
	if len(before) != 0 {
		t.Fatal("restart lost a persisted identity")
	}
	start := len(client.Actions())
	for range 3 {
		for _, original := range []HumanCredential{user, admin, otherKey} {
			got, err := humans.GetKey(ctx, original.HomelabUserID, original.Tier, original.Revision)
			if err != nil || got != original {
				t.Fatal("read-only lookup changed a persisted credential")
			}
			record, ok := restarted.LookupStored(got.Key)
			if !ok || record.HomelabUserID != got.HomelabUserID || record.AgentInstance != "" || record.Namespace != "" {
				t.Fatal("human was not authenticated as a distinct canonical principal")
			}
		}
	}
	for _, action := range client.Actions()[start:] {
		if action.GetVerb() != "list" && action.GetVerb() != "get" {
			t.Fatal("lookup wrote persistent state")
		}
	}
	if _, _, err := agents.Revoke(ctx, "", "agents", "", user.Key); !errors.Is(err, ErrKeyNotFound) {
		t.Fatal("agent raw-key revocation accepted a human principal")
	}
	if got, err := agents.Get(ctx, "hermes", "agents", manifest.TierAdmin); err != nil || got != agentKey {
		t.Fatal("human reconciliation changed agent semantics")
	}
}

func TestHumanKeysEligibilityAndLifecycle(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		state HumanState
		admin bool
		code  string
	}{
		{name: "admin role removed", state: HumanActive, code: "tier_ineligible"},
		{name: "disabled", state: HumanDisabled, code: "human_disabled"},
		{name: "deleted", state: HumanDeleted, code: "human_deleted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			humans, store, _ := newHumanFixture(t)
			identity, err := humans.Put(ctx, attestedHuman("human", true), 0)
			if err != nil {
				t.Fatal(err)
			}
			user, err := humans.Create(ctx, "human", manifest.TierUser, identity.Revision)
			if err != nil {
				t.Fatal(err)
			}
			admin, err := humans.Create(ctx, "human", manifest.TierAdmin, identity.Revision)
			if err != nil {
				t.Fatal(err)
			}
			identity.Revision = 0
			identity.State, identity.Admin, identity.SourceRevision = tc.state, tc.admin, "snapshot-2"
			changed, err := humans.Put(ctx, identity, 1)
			if err != nil {
				t.Fatal(err)
			}
			if changed.Revision != 2 {
				t.Fatal("eligibility transition did not advance revision")
			}
			if _, ok := store.LookupStored(admin.Key); ok {
				t.Fatal("withdrawn admin credential still authenticates")
			}
			if _, ok := store.LookupStored(user.Key); ok != (tc.state == HumanActive) {
				t.Fatal("transition withdrew or retained the wrong user credential")
			}
			_, err = humans.GetKey(ctx, "human", manifest.TierAdmin, changed.Revision)
			requireHumanCode(t, err, tc.code)
			_, err = humans.Create(ctx, "human", manifest.TierAdmin, changed.Revision)
			requireHumanCode(t, err, tc.code)
			if err := humans.Restore(ctx); err != nil {
				t.Fatal(err)
			}
			if _, ok := store.LookupStored(admin.Key); ok {
				t.Fatal("withdrawn admin credential revived on restore")
			}
			reactivate := attestedHuman("human", true)
			reactivate.SourceRevision = "snapshot-3"
			active, err := humans.Put(ctx, reactivate, changed.Revision)
			if tc.state == HumanDeleted {
				requireHumanCode(t, err, "human_deleted")
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = humans.GetKey(ctx, "human", manifest.TierAdmin, active.Revision)
			requireHumanCode(t, err, "key_not_found")
			reissued, err := humans.Create(ctx, "human", manifest.TierAdmin, active.Revision)
			if err != nil || reissued.Key == admin.Key {
				t.Fatal("explicit reissuance reused a revoked admin credential")
			}
			_, err = humans.GetKey(ctx, "human", manifest.TierUser, 1)
			requireHumanCode(t, err, "revision_conflict")
		})
	}
}

func TestHumanKeysRejectInvalidProvenanceAndRevisions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		edit func(*Human)
	}{
		{name: "missing canonical ID", edit: func(h *Human) { h.HomelabUserID = "" }},
		{name: "blank identity", edit: func(h *Human) { h.HomelabUserID = " \t" }},
		{name: "invalid identity encoding", edit: func(h *Human) { h.HomelabUserID = "\xff" }},
		{name: "identity exceeds storage bound", edit: func(h *Human) { h.HomelabUserID = strings.Repeat("x", 4097) }},
		{name: "missing source revision", edit: func(h *Human) { h.SourceRevision = "" }},
		{name: "untrusted source", edit: func(h *Human) { h.Source = "browser" }},
		{name: "invalid state", edit: func(h *Human) { h.State = "unknown" }},
		{name: "inactive admin", edit: func(h *Human) { h.State = HumanDisabled }},
		{name: "caller assigned revision", edit: func(h *Human) { h.Revision = 2 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			humans, store, client := newHumanFixture(t)
			input := attestedHuman("human", true)
			tc.edit(&input)
			start := len(client.Actions())
			_, err := humans.Put(context.Background(), input, 0)
			requireHumanCode(t, err, "invalid_arguments")
			if len(store.List()) != 0 || len(client.Actions()) != start {
				t.Fatal("invalid provenance reached persistence or authentication")
			}
		})
	}
	humans, _, client := newHumanFixture(t)
	input := attestedHuman(" opaque:Case/Sensitive.ID \u00e9 ", false)
	identity, err := humans.Put(context.Background(), input, 0)
	if err != nil || identity.HomelabUserID != input.HomelabUserID {
		t.Fatal("canonical ID was changed")
	}
	_, err = humans.Put(context.Background(), input, 0)
	requireHumanCode(t, err, "revision_conflict")
	start := len(client.Actions())
	again, err := humans.Put(context.Background(), input, identity.Revision)
	if err != nil || again != identity {
		t.Fatal("identical source snapshot was not idempotent")
	}
	for _, action := range client.Actions()[start:] {
		if action.GetVerb() != "list" {
			t.Fatal("identical snapshot wrote persistence")
		}
	}
	_, err = humans.Create(context.Background(), input.HomelabUserID, manifest.TierAdmin, identity.Revision)
	requireHumanCode(t, err, "tier_ineligible")
}

func TestHumanKeysReadAndWriteFailuresFailClosed(t *testing.T) {
	t.Parallel()
	for _, verb := range []string{"list", "create", "update"} {
		t.Run(verb, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			humans, store, client := newHumanFixture(t)
			identity, err := humans.Put(ctx, attestedHuman("human", true), 0)
			if err != nil {
				t.Fatal(err)
			}
			user, err := humans.Create(ctx, "human", manifest.TierUser, identity.Revision)
			if err != nil {
				t.Fatal(err)
			}
			agent, err := store.Mint("agent", manifest.TierUser)
			if err != nil {
				t.Fatal(err)
			}
			client.PrependReactor(verb, "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
				return true, nil, errors.New("sensitive backend diagnostic " + user.Key)
			})
			var result HumanCredential
			switch verb {
			case "list":
				result, err = humans.GetKey(ctx, "human", manifest.TierUser, identity.Revision)
				requireHumanCode(t, err, "storage_unavailable")
			case "create":
				_, err = humans.Put(ctx, attestedHuman("new-human", false), 0)
				requireHumanCode(t, err, "persistence_failed")
			case "update":
				result, err = humans.Create(ctx, "human", manifest.TierAdmin, identity.Revision)
				requireHumanCode(t, err, "persistence_failed")
			}
			if result.Key != "" || strings.Contains(err.Error(), user.Key) {
				t.Fatal("failed operation disclosed credential material")
			}
			if _, ok := store.LookupStored(user.Key); ok {
				t.Fatal("uncertain human persistence did not withdraw verification")
			}
			if _, ok := store.LookupStored(agent); !ok {
				t.Fatal("human failure withdrew an agent")
			}
			client.ReactionChain = client.ReactionChain[1:]
			if err := humans.Restore(ctx); err != nil {
				t.Fatal(err)
			}
			if _, ok := store.LookupStored(user.Key); !ok {
				t.Fatal("recovery did not restore the unchanged persisted user credential")
			}
			_, err = humans.GetKey(ctx, "human", manifest.TierAdmin, identity.Revision)
			requireHumanCode(t, err, "key_not_found")
		})
	}
}

func TestHumanKeysRejectDuplicateAndMalformedPersistence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		code string
		edit func(*corev1.Secret, *humanDocument)
		raw  string
		copy bool
	}{
		{name: "duplicate identity", code: "duplicate_record", copy: true},
		{name: "duplicate tier", code: "duplicate_record", edit: func(_ *corev1.Secret, d *humanDocument) { d.Keys = append(d.Keys, d.Keys[0]) }},
		{name: "invalid encoding", code: "invalid_record", edit: func(_ *corev1.Secret, d *humanDocument) { d.Keys[0].Key = "invalid" }},
		{name: "unattested admin", code: "invalid_record", edit: func(_ *corev1.Secret, d *humanDocument) { d.Human.Admin = false; d.Keys[0].Tier = manifest.TierAdmin }},
		{name: "inactive with keys", code: "invalid_record", edit: func(_ *corev1.Secret, d *humanDocument) { d.Human.State, d.Human.Admin = HumanDisabled, false }},
		{name: "version", code: "invalid_record", edit: func(_ *corev1.Secret, d *humanDocument) { d.Version = 2 }},
		{name: "wrong Secret type", code: "invalid_record", edit: func(s *corev1.Secret, _ *humanDocument) { s.Type = corev1.SecretTypeTLS }},
		{name: "workload owned", code: "invalid_record", edit: func(s *corev1.Secret, _ *humanDocument) { s.OwnerReferences = []metav1.OwnerReference{{Name: "pod"}} }},
		{name: "unknown field", code: "invalid_record", raw: `{"unexpected":"sensitive"}`},
		{name: "duplicate JSON field", code: "invalid_record", raw: `{"version":1,"version":1}`},
		{name: "parse failure", code: "invalid_record", raw: `{"keys":`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			humans, store, client := newHumanFixture(t)
			identity, err := humans.Put(ctx, attestedHuman("human", true), 0)
			if err != nil {
				t.Fatal(err)
			}
			credential, err := humans.Create(ctx, "human", manifest.TierUser, identity.Revision)
			if err != nil {
				t.Fatal(err)
			}
			secret, err := client.CoreV1().Secrets("gateway").Get(ctx, humanSecretName("human"), metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			var document humanDocument
			if err := json.Unmarshal(secret.Data[secretDataKey], &document); err != nil {
				t.Fatal("fixture document cannot be decoded")
			}
			if tc.copy {
				copy := secret.DeepCopy()
				copy.Name, copy.ResourceVersion = "duplicate", ""
				if _, err := client.CoreV1().Secrets("gateway").Create(ctx, copy, metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
			} else {
				if tc.edit != nil {
					tc.edit(secret, &document)
				}
				raw, err := json.Marshal(document)
				if err != nil {
					t.Fatal("fixture document cannot be encoded")
				}
				if tc.raw != "" {
					raw = []byte(tc.raw)
				}
				secret.Data[secretDataKey] = raw
				if _, err := client.CoreV1().Secrets("gateway").Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			got, err := humans.GetKey(ctx, "human", manifest.TierUser, identity.Revision)
			requireHumanCode(t, err, tc.code)
			if got.Key != "" || strings.Contains(err.Error(), credential.Key) {
				t.Fatal("corrupt record disclosed credentials")
			}
			if _, ok := store.LookupStored(credential.Key); ok {
				t.Fatal("corrupt record remained authenticated")
			}
			requireHumanCode(t, humans.Restore(ctx), tc.code)
		})
	}
}

func TestHumanKeysConcurrentWithdrawalVersusIssueAndLookup(t *testing.T) {
	t.Parallel()
	for _, state := range []HumanState{HumanActive, HumanDisabled} {
		for _, issue := range []bool{false, true} {
			name := string(state) + "/lookup"
			if issue {
				name = string(state) + "/issue"
			}

			t.Run(name, func(t *testing.T) {
				t.Parallel()
				ctx := context.Background()
				humans, store, client := newHumanFixture(t)
				identity, err := humans.Put(ctx, attestedHuman("human", true), 0)
				if err != nil {
					t.Fatal(err)
				}
				admin, err := humans.Create(ctx, "human", manifest.TierAdmin, identity.Revision)
				if err != nil {
					t.Fatal(err)
				}
				entered, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				client.PrependReactor("update", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
					once.Do(func() { close(entered); <-release })
					return false, nil, nil
				})
				change := attestedHuman("human", false)
				change.SourceRevision, change.State = "snapshot-2", state
				putDone := make(chan error, 1)
				go func() { _, err := humans.Put(ctx, change, identity.Revision); putDone <- err }()
				<-entered
				lookupDone := make(chan error, 1)
				go func() {
					var credential HumanCredential
					var err error
					if issue {
						credential, err = humans.Create(ctx, "human", manifest.TierAdmin, identity.Revision)
					} else {
						credential, err = humans.GetKey(ctx, "human", manifest.TierAdmin, identity.Revision)
					}
					if credential.Key != "" {
						err = errors.New("withdrawn credential was returned")
					}
					lookupDone <- err
				}()
				close(release)
				if err := <-putDone; err != nil {
					t.Fatal(err)
				}
				code := "tier_ineligible"
				if state == HumanDisabled {
					code = "human_disabled"
				}
				requireHumanCode(t, <-lookupDone, code)
				if _, ok := store.LookupStored(admin.Key); ok {
					t.Fatal("concurrent withdrawal left an admin verifier")
				}
				if err := humans.Restore(ctx); err != nil {
					t.Fatal(err)
				}
				if _, ok := store.LookupStored(admin.Key); ok {
					t.Fatal("concurrent withdrawal was not durable")
				}
			})
		}
	}
}

func TestHumanKeysIssuanceBeforeConcurrentWithdrawalIsNotRevived(t *testing.T) {
	t.Parallel()
	for _, state := range []HumanState{HumanActive, HumanDisabled} {
		t.Run(string(state), func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			humans, store, client := newHumanFixture(t)
			if _, err := humans.Put(ctx, attestedHuman("human", true), 0); err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			client.PrependReactor("update", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
				once.Do(func() { close(entered); <-release })
				return false, nil, nil
			})
			type issued struct {
				credential HumanCredential
				err        error
			}
			issueDone := make(chan issued, 1)
			go func() {
				credential, err := humans.Create(ctx, "human", manifest.TierAdmin, 1)
				issueDone <- issued{credential: credential, err: err}
			}()
			<-entered
			putDone := make(chan error, 1)
			go func() {
				input := attestedHuman("human", false)
				input.State, input.SourceRevision = state, "snapshot-2"
				_, err := humans.Put(ctx, input, 1)
				putDone <- err
			}()
			close(release)
			created := <-issueDone
			if created.err != nil || created.credential.Key == "" {
				t.Fatal("issuance preceding withdrawal failed")
			}
			if err := <-putDone; err != nil {
				t.Fatal(err)
			}
			if _, ok := store.LookupStored(created.credential.Key); ok {
				t.Fatal("later withdrawal did not remove the issued verifier")
			}
			if err := humans.Restore(ctx); err != nil {
				t.Fatal(err)
			}
			if _, ok := store.LookupStored(created.credential.Key); ok {
				t.Fatal("later withdrawal did not remove the persisted issued credential")
			}
		})
	}
}
