package keys

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/karlrissland/homelab-api2mcp/internal/manifest"
)

func TestAgentKeysSurviveRestartAndLifecycleChanges(t *testing.T) {
	t.Parallel()
	for _, bypass := range []bool{false, true} {
		name := "authenticated"
		if bypass {
			name = "auth bypass does not alter stored identities"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			client := fake.NewSimpleClientset()
			writer, err := NewSecretWriter(client)
			if err != nil {
				t.Fatal(err)
			}
			restart := func() (*AgentKeys, *Store) {
				store := NewStore()
				if bypass {
					store.DisableAuth()
				}
				if _, err := writer.EnsureBootstrap(ctx, "gateway", store); err != nil {
					t.Fatal(err)
				}
				agents := testAgentRepository(t, writer, store)
				if err := agents.Restore(ctx); err != nil {
					t.Fatal(err)
				}
				return agents, store
			}
			agents, store := restart()
			userKey, err := agents.Create(ctx, "hermes", "agents-a", manifest.TierUser)
			if err != nil {
				t.Fatal(err)
			}
			secondKey, err := agents.Create(ctx, "hermes", "agents-a", manifest.TierUser)
			if err != nil {
				t.Fatal(err)
			}
			adminKey, err := agents.Create(ctx, "hermes", "agents-a", manifest.TierAdmin)
			if err != nil {
				t.Fatal(err)
			}
			otherKey, err := agents.Create(ctx, "hermes", "agents-b", manifest.TierUser)
			if err != nil {
				t.Fatal(err)
			}
			original, ok := store.LookupStored(userKey)
			if !ok || original.Namespace != "agents-a" || original.CreatedAt.IsZero() {
				t.Fatalf("issued key metadata = %+v, found = %t", original, ok)
			}
			agents, store = restart()
			if got, ok := store.LookupStored(userKey); !ok || got != original {
				t.Fatalf("restart changed key metadata: %+v", got)
			}
			got, err := agents.Get(ctx, "hermes", "agents-a", manifest.TierUser)
			if err != nil || got != userKey {
				t.Fatal("Get did not recover the original key after restart")
			}
			got, created, err := agents.Ensure(ctx, "hermes", "agents-a", manifest.TierUser)
			if err != nil || created || got != userKey {
				t.Fatal("Ensure replaced an existing persisted key after restart")
			}
			if _, _, err := agents.Revoke(ctx, "", "agents-b", "", userKey); !errors.Is(err, ErrKeyNotFound) {
				t.Fatal("raw-key revocation crossed an agent namespace")
			}
			rotated, revoked, err := agents.Rotate(ctx, "hermes", "agents-a", manifest.TierUser)
			if err != nil || revoked != 2 || rotated == userKey || rotated == secondKey {
				t.Fatalf("Rotate revoked = %d, error = %v", revoked, err)
			}
			agents, store = restart()
			for key, accepted := range map[string]bool{userKey: false, secondKey: false, rotated: true, adminKey: true, otherKey: true} {
				if _, ok := store.LookupStored(key); ok != accepted {
					t.Fatal("rotation/restart changed the wrong credential")
				}
			}
			owner, revoked, err := agents.Revoke(ctx, "", "agents-a", "", adminKey)
			if err != nil || revoked != 1 || owner != "hermes" {
				t.Fatalf("Revoke(raw key) owner = %q, revoked = %d, error = %v", owner, revoked, err)
			}
			if _, revoked, err := agents.Revoke(ctx, "hermes", "agents-a", manifest.TierUser, ""); err != nil || revoked != 1 {
				t.Fatalf("Revoke(tier) revoked = %d, error = %v", revoked, err)
			}
			agents, store = restart()
			if _, err := agents.Get(ctx, "hermes", "agents-a", manifest.TierUser); !errors.Is(err, ErrKeyNotFound) {
				t.Fatal("revoked credential returned after restart")
			}
			if _, ok := store.LookupStored(adminKey); ok {
				t.Fatal("revoked admin credential returned after restart")
			}
			if _, ok := store.LookupStored(otherKey); !ok || len(store.List()) != 2 {
				t.Fatal("revocation removed another namespace's key or bootstrap identity")
			}
			for _, action := range client.Actions() {
				if action.GetNamespace() != "gateway" || action.GetVerb() == "delete" {
					t.Fatalf("key persistence used unexpected namespace/verb: %s %s", action.GetNamespace(), action.GetVerb())
				}
			}
			secret, err := client.CoreV1().Secrets("gateway").Get(ctx, AgentSecretName("hermes", "agents-a"), metav1.GetOptions{})
			if err != nil || len(secret.OwnerReferences) != 0 {
				t.Fatal("authoritative Secret was deleted or tied to a workload lifetime")
			}
		})
	}
}

func TestAgentKeysFailedWritesDoNotChangeVerification(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		verb string
		run  func(context.Context, *AgentKeys) error
	}{
		{name: "first create", verb: "create", run: func(ctx context.Context, a *AgentKeys) error {
			_, err := a.Create(ctx, "new-agent", "agents", manifest.TierUser)
			return err
		}},
		{name: "additional create", verb: "update", run: func(ctx context.Context, a *AgentKeys) error {
			_, err := a.Create(ctx, "hermes", "agents", manifest.TierUser)
			return err
		}},
		{name: "rotation", verb: "update", run: func(ctx context.Context, a *AgentKeys) error {
			_, _, err := a.Rotate(ctx, "hermes", "agents", manifest.TierUser)
			return err
		}},
		{name: "revocation", verb: "update", run: func(ctx context.Context, a *AgentKeys) error {
			_, _, err := a.Revoke(ctx, "hermes", "agents", manifest.TierUser, "")
			return err
		}},
		{name: "get denied", verb: "get", run: func(ctx context.Context, a *AgentKeys) error {
			_, err := a.Get(ctx, "hermes", "agents", manifest.TierUser)
			return err
		}},
		{name: "restore denied", verb: "list", run: func(ctx context.Context, a *AgentKeys) error {
			return a.Restore(ctx)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			client := fake.NewSimpleClientset()
			writer, err := NewSecretWriter(client)
			if err != nil {
				t.Fatal(err)
			}
			store := NewStore()
			agents := testAgentRepository(t, writer, store)
			key, err := agents.Create(ctx, "hermes", "agents", manifest.TierUser)
			if err != nil {
				t.Fatal(err)
			}
			before := store.List()
			denied := apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "test", errors.New("denied"))
			var candidate string
			client.PrependReactor(tc.verb, "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
				if action, ok := action.(interface{ GetObject() runtime.Object }); ok {
					var document agentDocument
					if err := json.Unmarshal(action.GetObject().(*corev1.Secret).Data[secretDataKey], &document); err != nil {
						return true, nil, err
					}
					for _, entry := range document.Keys {
						if entry.Key != key {
							candidate = entry.Key
						}
					}
				}
				return true, nil, denied
			})
			if err := tc.run(ctx, agents); !errors.Is(err, denied) {
				t.Fatalf("mutation error = %v, want explicit API failure", err)
			}
			if !reflect.DeepEqual(before, store.List()) {
				t.Fatal("failed persistence changed the live verification index")
			}
			if candidate != "" {
				if _, ok := store.LookupStored(candidate); ok {
					t.Fatal("unpersisted candidate key became authorized")
				}
			}
			if tc.verb != "list" {
				restarted := NewStore()
				if err := testAgentRepository(t, writer, restarted).Restore(ctx); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(before, restarted.List()) {
					t.Fatal("failed mutation changed durable credentials")
				}
			}
		})
	}
}

func TestAgentKeysRestoreRejectsCorruptStateAtomically(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		edit func(*corev1.Secret, *agentDocument)
		raw  []byte
	}{
		{name: "malformed JSON", raw: []byte(`{"keys":`)},
		{name: "unknown version", edit: func(_ *corev1.Secret, d *agentDocument) { d.Version = 2 }},
		{name: "missing keys array", edit: func(_ *corev1.Secret, d *agentDocument) { d.Keys = nil }},
		{name: "wrong owner", edit: func(_ *corev1.Secret, d *agentDocument) { d.Namespace = "elsewhere" }},
		{name: "invalid encoding", edit: func(_ *corev1.Secret, d *agentDocument) { d.Keys[0].Key = "invalid-key" }},
		{name: "tier contains credential", edit: func(_ *corev1.Secret, d *agentDocument) { d.Keys[0].Tier = manifest.Tier(d.Keys[0].Key) }},
		{name: "missing time", edit: func(_ *corev1.Secret, d *agentDocument) { d.Keys[0].CreatedAt = time.Time{} }},
		{name: "duplicate key", edit: func(_ *corev1.Secret, d *agentDocument) { d.Keys = append(d.Keys, d.Keys[0]) }},
		{name: "unexpected type", edit: func(s *corev1.Secret, _ *agentDocument) { s.Type = corev1.SecretTypeTLS }},
		{name: "workload owner", edit: func(s *corev1.Secret, _ *agentDocument) { s.OwnerReferences = []metav1.OwnerReference{{Name: "pod"}} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			client := fake.NewSimpleClientset()
			writer, err := NewSecretWriter(client)
			if err != nil {
				t.Fatal(err)
			}
			source := NewStore()
			agents := testAgentRepository(t, writer, source)
			goodKey, err := agents.Create(ctx, "alice", "agents", manifest.TierUser)
			if err != nil {
				t.Fatal(err)
			}
			badKey, err := agents.Create(ctx, "bob", "agents", manifest.TierAdmin)
			if err != nil {
				t.Fatal(err)
			}
			secret, err := client.CoreV1().Secrets("gateway").Get(ctx, AgentSecretName("bob", "agents"), metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			var document agentDocument
			if err := json.Unmarshal(secret.Data[secretDataKey], &document); err != nil {
				t.Fatal(err)
			}
			if tc.edit != nil {
				tc.edit(secret, &document)
			}
			raw, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			secret.Data[secretDataKey] = raw
			if tc.raw != nil {
				secret.Data[secretDataKey] = tc.raw
			}
			if _, err := client.CoreV1().Secrets("gateway").Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			restarted := NewStore()
			bootstrap, err := restarted.Mint("cluster-admin", manifest.TierAdmin)
			if err != nil {
				t.Fatal(err)
			}
			err = testAgentRepository(t, writer, restarted).Restore(ctx)
			if err == nil {
				t.Fatal("corrupt persisted state was accepted")
			}
			if strings.Contains(err.Error(), badKey) || strings.Contains(err.Error(), goodKey) {
				t.Fatal("restore failure exposed key material")
			}
			if _, ok := restarted.LookupStored(bootstrap); !ok || len(restarted.List()) != 1 {
				t.Fatal("failed restore partially changed the verification index")
			}
		})
	}
}

func TestAgentKeysConcurrentIssuanceKeepsEveryKey(t *testing.T) {
	t.Parallel()
	client := fake.NewSimpleClientset()
	writer, err := NewSecretWriter(client)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	agents := testAgentRepository(t, writer, store)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range cap(errs) {
		wg.Go(func() {
			_, err := agents.Create(context.Background(), "hermes", "agents", manifest.TierUser)
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	restarted := NewStore()
	if err := testAgentRepository(t, writer, restarted).Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.List()) != 8 || !reflect.DeepEqual(store.List(), restarted.List()) {
		t.Fatal("concurrent issuance lost a durable key")
	}
}

func TestAgentKeysConflictDoesNotOverwriteConcurrentUpdate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	writer, err := NewSecretWriter(client)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	agents := testAgentRepository(t, writer, store)
	original, err := agents.Create(ctx, "hermes", "agents", manifest.TierUser)
	if err != nil {
		t.Fatal(err)
	}
	external, err := store.generateKey()
	if err != nil {
		t.Fatal(err)
	}
	conflictOnce := true
	var candidate string
	client.PrependReactor("update", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if !conflictOnce {
			return false, nil, nil
		}
		conflictOnce = false
		submitted := action.(k8stesting.UpdateAction).GetObject().(*corev1.Secret)
		var document agentDocument
		if err := json.Unmarshal(submitted.Data[secretDataKey], &document); err != nil {
			return true, nil, err
		}
		candidate = document.Keys[len(document.Keys)-1].Key
		document.Keys = []agentKey{
			document.Keys[0],
			{Key: external, Tier: manifest.TierUser, CreatedAt: time.Now().UTC()},
		}
		raw, err := json.Marshal(document)
		if err != nil {
			return true, nil, err
		}
		concurrent := submitted.DeepCopy()
		concurrent.Data[secretDataKey] = raw
		if err := client.Tracker().Update(corev1.SchemeGroupVersion.WithResource("secrets"), concurrent, "gateway"); err != nil {
			return true, nil, err
		}
		return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "secrets"}, submitted.Name, errors.New("concurrent update"))
	})
	if _, err := agents.Create(ctx, "hermes", "agents", manifest.TierUser); !apierrors.IsConflict(err) {
		t.Fatalf("concurrent update did not return an explicit conflict: %v", err)
	}
	if _, ok := store.LookupStored(candidate); ok || len(store.List()) != 1 {
		t.Fatal("conflicting write authorized an unpersisted key")
	}
	if _, err := agents.Create(ctx, "hermes", "agents", manifest.TierUser); err != nil {
		t.Fatal(err)
	}
	restarted := NewStore()
	if err := testAgentRepository(t, writer, restarted).Restore(ctx); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{original, external} {
		if _, ok := restarted.LookupStored(key); !ok {
			t.Fatal("retry overwrote a previously committed key")
		}
	}
	if len(restarted.List()) != 3 {
		t.Fatal("retry failed to preserve the concurrent update and new key")
	}
}

func testAgentRepository(t *testing.T, writer *SecretWriter, store *Store) *AgentKeys {
	t.Helper()
	agents, err := NewAgentKeys(writer, "gateway", store)
	if err != nil {
		t.Fatal(err)
	}
	return agents
}
