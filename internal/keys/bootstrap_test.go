package keys

import (
	"context"
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

func TestBootstrapCreateAndRestore(t *testing.T) {
	t.Parallel()

	for _, disabled := range []bool{false, true} {
		name := "authenticated"
		if disabled {
			name = "auth bypass still persists key"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			client := fake.NewSimpleClientset()
			writer, err := NewSecretWriter(client)
			if err != nil {
				t.Fatal(err)
			}
			store := NewStore()
			if disabled {
				store.DisableAuth()
			}
			if created, err := writer.EnsureBootstrap(ctx, "gateway", store); err != nil || !created {
				t.Fatalf("EnsureBootstrap(first) created = %t, error = %v", created, err)
			}
			secret, err := client.CoreV1().Secrets("gateway").Get(ctx, BootstrapSecretName, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			key := string(secret.Data[BootstrapSecretDataKey])
			if key == "" || len(secret.OwnerReferences) != 0 {
				t.Fatal("bootstrap Secret must contain a key and outlive the pod/deployment")
			}
			if created, err := writer.EnsureBootstrap(ctx, "gateway", store); err != nil || created {
				t.Fatalf("EnsureBootstrap(repeated) created = %t, error = %v", created, err)
			}
			if records := store.List(); len(records) != 1 || records[0].AgentInstance != bootstrapAdminAgent || records[0].Tier != manifest.TierAdmin {
				t.Fatalf("bootstrap verification records = %+v, want one cluster-admin record", records)
			}
			restarted := NewStore()
			if created, err := writer.EnsureBootstrap(ctx, "gateway", restarted); err != nil || created {
				t.Fatalf("EnsureBootstrap(restart) created = %t, error = %v", created, err)
			}
			if record, ok := restarted.Lookup(key); !ok || record.AgentInstance != bootstrapAdminAgent || record.Tier != manifest.TierAdmin {
				t.Fatalf("restored key identity = %+v, found = %t", record, ok)
			}
			for _, action := range client.Actions() {
				if action.GetVerb() != "get" && action.GetVerb() != "create" {
					t.Fatalf("unexpected bootstrap Kubernetes action: %s", action.GetVerb())
				}
			}
		})
	}
}

func TestBootstrapConcurrentCreation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := fake.NewSimpleClientset()
	writer, err := NewSecretWriter(client)
	if err != nil {
		t.Fatal(err)
	}
	stores := []*Store{NewStore(), NewStore(), NewStore(), NewStore()}
	type result struct {
		created bool
		err     error
	}
	results := make(chan result, len(stores))
	var wg sync.WaitGroup
	for _, store := range stores {
		wg.Go(func() {
			created, err := writer.EnsureBootstrap(ctx, "gateway", store)
			results <- result{created, err}
		})
	}
	wg.Wait()
	close(results)
	createdCount := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.created {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Fatalf("%d startups created the Secret, want 1", createdCount)
	}
	secret, err := client.CoreV1().Secrets("gateway").Get(ctx, BootstrapSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, store := range stores {
		if _, ok := store.Lookup(string(secret.Data[BootstrapSecretDataKey])); !ok || len(store.List()) != 1 {
			t.Fatal("concurrent startup did not authorize only the persisted key")
		}
	}
}

func TestBootstrapLoadsConcurrentWinner(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	winner := bootstrapTestSecret()
	client := fake.NewSimpleClientset()
	var candidate string
	client.PrependReactor("create", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		candidate = string(action.(k8stesting.CreateAction).GetObject().(*corev1.Secret).Data[BootstrapSecretDataKey])
		if err := client.Tracker().Create(corev1.SchemeGroupVersion.WithResource("secrets"), winner, "gateway"); err != nil {
			return true, nil, err
		}
		return true, nil, apierrors.NewAlreadyExists(schema.GroupResource{Resource: "secrets"}, BootstrapSecretName)
	})
	writer, err := NewSecretWriter(client)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	if created, err := writer.EnsureBootstrap(ctx, "gateway", store); err != nil || created {
		t.Fatalf("EnsureBootstrap(lost race) created = %t, error = %v", created, err)
	}
	if _, ok := store.Lookup(candidate); ok || candidate == "" {
		t.Fatal("unused candidate was authorized or creation was not exercised")
	}
	record, ok := store.Lookup(string(winner.Data[BootstrapSecretDataKey]))
	if !ok || !record.CreatedAt.Equal(winner.CreationTimestamp.Time) || len(store.List()) != 1 {
		t.Fatalf("persisted winner record = %+v, found = %t", record, ok)
	}
}

func TestBootstrapRejectsMalformedSecret(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		edit func(*corev1.Secret)
	}{
		{name: "missing data", edit: func(s *corev1.Secret) { s.Data = nil }},
		{name: "missing key", edit: func(s *corev1.Secret) { delete(s.Data, BootstrapSecretDataKey) }},
		{name: "empty key", edit: func(s *corev1.Secret) { s.Data[BootstrapSecretDataKey] = nil }},
		{name: "invalid key", edit: func(s *corev1.Secret) { s.Data[BootstrapSecretDataKey] = []byte("not-a-valid-key") }},
		{name: "wrong type", edit: func(s *corev1.Secret) { s.Type = corev1.SecretTypeTLS }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			secret := bootstrapTestSecret()
			tc.edit(secret)
			client := fake.NewSimpleClientset(secret)
			writer, err := NewSecretWriter(client)
			if err != nil {
				t.Fatal(err)
			}
			store := NewStore()
			if created, err := writer.EnsureBootstrap(context.Background(), "gateway", store); err == nil || created {
				t.Fatalf("EnsureBootstrap(invalid) created = %t, error = %v", created, err)
			}
			if len(store.List()) != 0 {
				t.Fatal("invalid Secret authorized a bootstrap key")
			}
			persisted, err := client.CoreV1().Secrets("gateway").Get(context.Background(), BootstrapSecretName, metav1.GetOptions{})
			if err != nil || !reflect.DeepEqual(persisted, secret) {
				t.Fatal("malformed Secret was overwritten")
			}
			for _, action := range client.Actions() {
				if action.GetVerb() != "get" {
					t.Fatalf("malformed Secret triggered action %s", action.GetVerb())
				}
			}
		})
	}
}

func TestBootstrapAPIFailures(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		verb string
		err  error
	}{
		{name: "read denied", verb: "get", err: apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, BootstrapSecretName, errors.New("denied"))},
		{name: "create denied", verb: "create", err: apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, BootstrapSecretName, errors.New("denied"))},
		{name: "read unavailable", verb: "get", err: errors.New("API unavailable")},
		{name: "create unavailable", verb: "create", err: errors.New("API unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := fake.NewSimpleClientset()
			var candidate string
			client.PrependReactor(tc.verb, "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
				if tc.verb == "create" {
					candidate = string(action.(k8stesting.CreateAction).GetObject().(*corev1.Secret).Data[BootstrapSecretDataKey])
				}
				return true, nil, tc.err
			})
			writer, err := NewSecretWriter(client)
			if err != nil {
				t.Fatal(err)
			}
			store := NewStore()
			created, err := writer.EnsureBootstrap(context.Background(), "gateway", store)
			if created || !errors.Is(err, tc.err) {
				t.Fatalf("EnsureBootstrap(failure) created = %t, error = %v", created, err)
			}
			if len(store.List()) != 0 {
				t.Fatal("failed initialization left an authorized key")
			}
			if candidate != "" && strings.Contains(err.Error(), candidate) {
				t.Fatal("initialization error disclosed the candidate key")
			}
		})
	}
}

func bootstrapTestSecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: BootstrapSecretName, Namespace: "gateway",
			CreationTimestamp: metav1.NewTime(time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)),
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{BootstrapSecretDataKey: []byte(strings.Repeat("A", 43))},
	}
}
