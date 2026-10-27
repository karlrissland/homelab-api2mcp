package keys

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/karlrissland/homelab-api2mcp/internal/manifest"
)

// BootstrapSecretName is the stable gateway-namespace Secret read by provisioners.
const BootstrapSecretName = "mcp2rest-bootstrap-admin"

// BootstrapSecretDataKey holds the bootstrap bearer key in BootstrapSecretName.
const BootstrapSecretDataKey = "key"

const bootstrapAdminAgent = "cluster-admin"

// EnsureBootstrap creates or loads the persistent admin credential and imports
// it into store before the gateway accepts requests. It never rotates an existing
// Secret or returns the raw key. The result reports whether this call created it.
func (w *SecretWriter) EnsureBootstrap(ctx context.Context, namespace string, store *Store) (bool, error) {
	if namespace == "" {
		return false, fmt.Errorf("bootstrap admin key: namespace is required")
	}
	if store == nil {
		return false, fmt.Errorf("bootstrap admin key: key store is required")
	}
	secrets := w.client.CoreV1().Secrets(namespace)
	secret, err := secrets.Get(ctx, BootstrapSecretName, metav1.GetOptions{})
	created := false
	if apierrors.IsNotFound(err) {
		key, keyErr := store.generateKey()
		if keyErr != nil {
			return false, fmt.Errorf("bootstrap admin key: %w", keyErr)
		}
		secret, err = secrets.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: BootstrapSecretName, Namespace: namespace},
			Type:       corev1.SecretTypeOpaque,
			Data:       map[string][]byte{BootstrapSecretDataKey: []byte(key)},
		}, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			// A concurrent startup won creation. Only its persisted key is
			// authoritative; our unused candidate was never imported.
			secret, err = secrets.Get(ctx, BootstrapSecretName, metav1.GetOptions{})
		} else {
			created = err == nil
		}
	}
	if err != nil {
		return false, fmt.Errorf("bootstrap admin key: get/create Secret %s/%s: %w", namespace, BootstrapSecretName, err)
	}
	if secret.Type != corev1.SecretTypeOpaque {
		return false, fmt.Errorf("bootstrap admin key: Secret %s/%s must have type Opaque", namespace, BootstrapSecretName)
	}
	createdAt := secret.CreationTimestamp.Time
	if createdAt.IsZero() {
		createdAt = store.now().UTC()
	}
	if err := store.Import(string(secret.Data[BootstrapSecretDataKey]), Record{
		AgentInstance: bootstrapAdminAgent,
		Tier:          manifest.TierAdmin,
		CreatedAt:     createdAt,
	}); err != nil {
		return false, fmt.Errorf("bootstrap admin key: invalid Secret %s/%s data[%q]: %w", namespace, BootstrapSecretName, BootstrapSecretDataKey, err)
	}
	return created, nil
}
