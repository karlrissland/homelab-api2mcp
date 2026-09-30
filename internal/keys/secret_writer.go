package keys

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const secretDataKey = "keys.json"

// SecretWriter writes per-instance mcp2rest key material into Kubernetes
// Secrets using an injected client-go interface for testability.
type SecretWriter struct {
	client kubernetes.Interface
}

// NewSecretWriter builds a SecretWriter around the provided Kubernetes client.
func NewSecretWriter(client kubernetes.Interface) (*SecretWriter, error) {
	if client == nil {
		return nil, fmt.Errorf("new secret writer: kubernetes client is required")
	}
	return &SecretWriter{client: client}, nil
}

// SecretName returns the per-instance Secret name mandated by the Phase 5 plan.
func SecretName(agentInstance string) string {
	return fmt.Sprintf("%s-mcp2rest-keys", agentInstance)
}

// UpsertSecret creates or updates the per-instance Secret in the target namespace.
func (w *SecretWriter) UpsertSecret(ctx context.Context, agentInstance, namespace string, keys []string) error {
	if agentInstance == "" {
		return fmt.Errorf("upsert secret: agent instance is required")
	}
	if namespace == "" {
		return fmt.Errorf("upsert secret for agent instance %q: namespace is required", agentInstance)
	}
	if len(keys) == 0 {
		return fmt.Errorf("upsert secret for agent instance %q in namespace %q: at least one key is required", agentInstance, namespace)
	}
	for i, key := range keys {
		if key == "" {
			return fmt.Errorf("upsert secret for agent instance %q in namespace %q: key %d is empty", agentInstance, namespace, i)
		}
	}

	encodedKeys, err := json.Marshal(keys)
	if err != nil {
		return fmt.Errorf("upsert secret for agent instance %q in namespace %q: marshal keys: %w", agentInstance, namespace, err)
	}

	name := SecretName(agentInstance)
	secrets := w.client.CoreV1().Secrets(namespace)
	desiredData := map[string][]byte{
		secretDataKey: encodedKeys,
	}

	secret, err := secrets.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("upsert secret %q in namespace %q: get existing secret: %w", name, namespace, err)
		}

		_, err = secrets.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
			Type: corev1.SecretTypeOpaque,
			Data: desiredData,
		}, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("upsert secret %q in namespace %q: create secret: %w", name, namespace, err)
		}
		return nil
	}

	secret.Type = corev1.SecretTypeOpaque
	secret.Data = desiredData
	if _, err := secrets.Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("upsert secret %q in namespace %q: update secret: %w", name, namespace, err)
	}

	return nil
}
