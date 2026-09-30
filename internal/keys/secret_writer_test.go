package keys

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestSecretWriterUpsertSecret(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := fake.NewSimpleClientset()
	writer, err := NewSecretWriter(client)
	if err != nil {
		t.Fatalf("NewSecretWriter() error = %v", err)
	}

	namespace := "agents"
	agentInstance := "hermes-alice"

	if err := writer.UpsertSecret(ctx, agentInstance, namespace, []string{"key-1", "key-2"}); err != nil {
		t.Fatalf("UpsertSecret(create) error = %v", err)
	}

	secret, err := client.CoreV1().Secrets(namespace).Get(ctx, SecretName(agentInstance), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get(created secret) error = %v", err)
	}
	if secret.Name != SecretName(agentInstance) {
		t.Fatalf("secret.Name = %q, want %q", secret.Name, SecretName(agentInstance))
	}
	if secret.Namespace != namespace {
		t.Fatalf("secret.Namespace = %q, want %q", secret.Namespace, namespace)
	}
	if secret.Type != corev1.SecretTypeOpaque {
		t.Fatalf("secret.Type = %q, want %q", secret.Type, corev1.SecretTypeOpaque)
	}
	if got := string(secret.Data[secretDataKey]); got != `["key-1","key-2"]` {
		t.Fatalf("secret.Data[%q] = %q, want %q", secretDataKey, got, `["key-1","key-2"]`)
	}

	if err := writer.UpsertSecret(ctx, agentInstance, namespace, []string{"key-3"}); err != nil {
		t.Fatalf("UpsertSecret(update) error = %v", err)
	}

	secrets, err := client.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("List(secrets) error = %v", err)
	}
	if len(secrets.Items) != 1 {
		t.Fatalf("len(secrets.Items) = %d, want 1", len(secrets.Items))
	}

	updatedSecret, err := client.CoreV1().Secrets(namespace).Get(ctx, SecretName(agentInstance), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get(updated secret) error = %v", err)
	}
	if got := string(updatedSecret.Data[secretDataKey]); got != `["key-3"]` {
		t.Fatalf("updated secret.Data[%q] = %q, want %q", secretDataKey, got, `["key-3"]`)
	}
}

func TestSecretWriterUpsertSecretValidation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := fake.NewSimpleClientset()
	writer, err := NewSecretWriter(client)
	if err != nil {
		t.Fatalf("NewSecretWriter() error = %v", err)
	}

	cases := []struct {
		name          string
		agentInstance string
		namespace     string
		keys          []string
	}{
		{name: "missing agent instance", namespace: "agents", keys: []string{"key-1"}},
		{name: "missing namespace", agentInstance: "hermes-alice", keys: []string{"key-1"}},
		{name: "missing keys", agentInstance: "hermes-alice", namespace: "agents"},
		{name: "empty key", agentInstance: "hermes-alice", namespace: "agents", keys: []string{"key-1", ""}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if err := writer.UpsertSecret(ctx, tc.agentInstance, tc.namespace, tc.keys); err == nil {
				t.Fatal("UpsertSecret() error = nil, want error")
			}
		})
	}
}

func TestNewSecretWriterRequiresClient(t *testing.T) {
	t.Parallel()

	writer, err := NewSecretWriter(nil)
	if err == nil {
		t.Fatal("NewSecretWriter(nil) error = nil, want error")
	}
	if writer != nil {
		t.Fatal("NewSecretWriter(nil) writer != nil")
	}
}
