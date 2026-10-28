package keys

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/karlrissland/homelab-api2mcp/internal/manifest"
)

// AgentSecretLabel selects authoritative agent-key Secrets in the gateway namespace.
const AgentSecretLabel = "mcp2rest.homelab.dev/agent-keys"

// AgentKeys persists agent credentials and reconciles their verification records.
// Mutations are serialized locally and committed to Kubernetes before changing
// the verification index. Multiple active gateways still need cache coordination.
type AgentKeys struct {
	mu        sync.Mutex
	writer    *SecretWriter
	namespace string
	store     *Store
}

type agentDocument struct {
	Version       int        `json:"version"`
	AgentInstance string     `json:"agentInstance"`
	Namespace     string     `json:"namespace"`
	Keys          []agentKey `json:"keys"`
}

type agentKey struct {
	Key       string        `json:"key"`
	Tier      manifest.Tier `json:"tier"`
	CreatedAt time.Time     `json:"createdAt"`
}

type issueAction int

const (
	createAgentKey issueAction = iota
	ensureAgentKey
	rotateAgentKey
)

type issueResult struct {
	key     string
	created bool
	revoked int
}

// NewAgentKeys constructs a credential repository in the gateway namespace.
func NewAgentKeys(writer *SecretWriter, namespace string, store *Store) (*AgentKeys, error) {
	if writer == nil || store == nil || strings.TrimSpace(namespace) == "" {
		return nil, fmt.Errorf("new agent keys: Secret writer, gateway namespace, and key store are required")
	}
	return &AgentKeys{writer: writer, namespace: namespace, store: store}, nil
}

// AgentSecretName deterministically identifies a namespace-qualified agent.
func AgentSecretName(agentInstance, namespace string) string {
	digest := sha256.Sum256([]byte(namespace + "\x00" + agentInstance))
	return "mcp2rest-agent-" + hex.EncodeToString(digest[:16])
}

// Restore validates all persisted agent credentials before atomically rebuilding
// their verification index. Bootstrap credentials are preserved.
func (a *AgentKeys) Restore(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	secrets, err := a.writer.client.CoreV1().Secrets(a.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: AgentSecretLabel + "=true",
	})
	if err != nil {
		return fmt.Errorf("restore agent keys in namespace %q: list Secrets: %w", a.namespace, err)
	}
	records := make(map[[sha256.Size]byte]Record)
	for i := range secrets.Items {
		_, indexed, err := a.decode(&secrets.Items[i])
		if err != nil {
			return err
		}
		for digest, record := range indexed {
			if _, duplicate := records[digest]; duplicate {
				return fmt.Errorf("restore agent keys: duplicate key across Secrets")
			}
			records[digest] = record
		}
	}
	return a.store.replaceAgents("", "", records)
}

// Create issues an additional key and durably stores its authorization metadata.
func (a *AgentKeys) Create(ctx context.Context, agentInstance, namespace string, tier manifest.Tier) (string, error) {
	result, err := a.issue(ctx, agentInstance, namespace, tier, createAgentKey)
	return result.key, err
}

// Ensure reuses an existing key for this identity and tier or creates one.
func (a *AgentKeys) Ensure(ctx context.Context, agentInstance, namespace string, tier manifest.Tier) (string, bool, error) {
	result, err := a.issue(ctx, agentInstance, namespace, tier, ensureAgentKey)
	return result.key, result.created, err
}

// Rotate durably replaces every key for one identity and tier.
func (a *AgentKeys) Rotate(ctx context.Context, agentInstance, namespace string, tier manifest.Tier) (string, int, error) {
	result, err := a.issue(ctx, agentInstance, namespace, tier, rotateAgentKey)
	return result.key, result.revoked, err
}

func (a *AgentKeys) issue(ctx context.Context, agentInstance, namespace string, tier manifest.Tier, action issueAction) (issueResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := validateAgent(agentInstance, namespace, tier); err != nil {
		return issueResult{}, err
	}
	secret, document, err := a.load(ctx, agentInstance, namespace)
	if err != nil {
		return issueResult{}, err
	}
	result := issueResult{}
	kept := make([]agentKey, 0, len(document.Keys)+1)
	for _, entry := range document.Keys {
		if entry.Tier == tier {
			if action == ensureAgentKey {
				if err := a.apply(document); err != nil {
					return issueResult{}, err
				}
				return issueResult{key: entry.Key}, nil
			}
			if action == rotateAgentKey {
				result.revoked++
				continue
			}
		}
		kept = append(kept, entry)
	}
	key, err := a.store.generateKey()
	if err != nil {
		return issueResult{}, fmt.Errorf("issue agent key: %w", err)
	}
	document.Keys = append(kept, agentKey{Key: key, Tier: tier, CreatedAt: a.store.now().UTC()})
	if err := a.save(ctx, secret, document); err != nil {
		return issueResult{}, err
	}
	result.key, result.created = key, true
	return result, nil
}

// Get returns an existing key for one identity and tier.
func (a *AgentKeys) Get(ctx context.Context, agentInstance, namespace string, tier manifest.Tier) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := validateAgent(agentInstance, namespace, tier); err != nil {
		return "", err
	}
	_, document, err := a.load(ctx, agentInstance, namespace)
	if err != nil {
		return "", err
	}
	for _, entry := range document.Keys {
		if entry.Tier == tier {
			if err := a.apply(document); err != nil {
				return "", err
			}
			return entry.Key, nil
		}
	}
	return "", fmt.Errorf("get agent key for %s/%s (%s): %w", namespace, agentInstance, tier, ErrKeyNotFound)
}

// Revoke durably removes one raw key, or every key of the requested tier.
// Empty agentInstance is resolved from the raw key's real verification record,
// never from Lookup's optional authentication bypass.
func (a *AgentKeys) Revoke(ctx context.Context, agentInstance, namespace string, tier manifest.Tier, rawKey string) (string, int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if rawKey != "" {
		record, ok := a.store.LookupStored(rawKey)
		if !ok || record.Namespace != namespace || (agentInstance != "" && record.AgentInstance != agentInstance) {
			return "", 0, fmt.Errorf("revoke agent key: %w", ErrKeyNotFound)
		}
		agentInstance, tier = record.AgentInstance, record.Tier
	}
	if err := validateAgent(agentInstance, namespace, tier); err != nil {
		return "", 0, err
	}
	secret, document, err := a.load(ctx, agentInstance, namespace)
	if err != nil {
		return "", 0, err
	}
	kept := make([]agentKey, 0, len(document.Keys))
	revoked := 0
	for _, entry := range document.Keys {
		if (rawKey != "" && entry.Key == rawKey) || (rawKey == "" && entry.Tier == tier) {
			revoked++
		} else {
			kept = append(kept, entry)
		}
	}
	if revoked == 0 {
		return "", 0, fmt.Errorf("revoke agent key: %w", ErrKeyNotFound)
	}
	document.Keys = kept
	if err := a.save(ctx, secret, document); err != nil {
		return "", 0, err
	}
	return agentInstance, revoked, nil
}

func validateAgent(agentInstance, namespace string, tier manifest.Tier) error {
	if len(validation.IsDNS1123Subdomain(agentInstance)) != 0 || len(validation.IsDNS1123Label(namespace)) != 0 || !tier.Valid() {
		return fmt.Errorf("agent key: valid Kubernetes agent/namespace names and a user/admin tier are required")
	}
	return nil
}

func (a *AgentKeys) load(ctx context.Context, agentInstance, namespace string) (*corev1.Secret, agentDocument, error) {
	secret, err := a.writer.client.CoreV1().Secrets(a.namespace).Get(ctx, AgentSecretName(agentInstance, namespace), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, agentDocument{Version: 1, AgentInstance: agentInstance, Namespace: namespace, Keys: []agentKey{}}, nil
	}
	if err != nil {
		return nil, agentDocument{}, fmt.Errorf("read agent key Secret: %w", err)
	}
	document, _, err := a.decode(secret)
	if err == nil && (document.AgentInstance != agentInstance || document.Namespace != namespace) {
		err = fmt.Errorf("agent key Secret identity does not match request")
	}
	return secret, document, err
}

func (a *AgentKeys) decode(secret *corev1.Secret) (agentDocument, map[[sha256.Size]byte]Record, error) {
	if secret.Namespace != a.namespace || secret.Type != corev1.SecretTypeOpaque || secret.Labels[AgentSecretLabel] != "true" || len(secret.OwnerReferences) != 0 {
		return agentDocument{}, nil, fmt.Errorf("invalid agent key Secret %s/%s: expected an independently owned, labeled Opaque Secret", secret.Namespace, secret.Name)
	}
	var document agentDocument
	if err := json.Unmarshal(secret.Data[secretDataKey], &document); err != nil {
		// Decoder diagnostics can contain document values, including credentials.
		return document, nil, fmt.Errorf("invalid agent key Secret %s/%s: malformed key document", secret.Namespace, secret.Name)
	}
	if document.Version != 1 || validateAgent(document.AgentInstance, document.Namespace, manifest.TierUser) != nil || document.Keys == nil ||
		secret.Name != AgentSecretName(document.AgentInstance, document.Namespace) {
		return document, nil, fmt.Errorf("invalid agent key Secret %s/%s: version, identity, name, or keys array is invalid", secret.Namespace, secret.Name)
	}
	records, err := a.index(document)
	if err != nil {
		return document, nil, fmt.Errorf("invalid agent key Secret %s/%s: %w", secret.Namespace, secret.Name, err)
	}
	return document, records, nil
}

func (a *AgentKeys) index(document agentDocument) (map[[sha256.Size]byte]Record, error) {
	records := make(map[[sha256.Size]byte]Record, len(document.Keys))
	for _, entry := range document.Keys {
		record := Record{AgentInstance: document.AgentInstance, Namespace: document.Namespace, Tier: entry.Tier, CreatedAt: entry.CreatedAt}
		if err := a.store.validate(entry.Key, record); err != nil {
			return nil, fmt.Errorf("invalid persisted agent key encoding or authorization metadata")
		}
		digest := digestKey(entry.Key)
		if _, duplicate := records[digest]; duplicate {
			return nil, fmt.Errorf("duplicate key in agent key document")
		}
		records[digest] = record
	}
	return records, nil
}

func (a *AgentKeys) apply(document agentDocument) error {
	records, err := a.index(document)
	if err != nil {
		return err
	}
	return a.store.replaceAgents(document.Namespace, document.AgentInstance, records)
}

func (a *AgentKeys) save(ctx context.Context, secret *corev1.Secret, document agentDocument) error {
	records, err := a.index(document)
	if err != nil {
		return err
	}
	if err := a.store.checkRecords(records); err != nil {
		return err
	}
	raw, err := json.Marshal(document)
	if err != nil {
		return fmt.Errorf("encode agent key document: %w", err)
	}
	var persisted *corev1.Secret
	if secret == nil {
		persisted, err = a.writer.client.CoreV1().Secrets(a.namespace).Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: AgentSecretName(document.AgentInstance, document.Namespace), Namespace: a.namespace,
				Labels: map[string]string{AgentSecretLabel: "true"},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{secretDataKey: raw},
		}, metav1.CreateOptions{})
	} else {
		secret.Data[secretDataKey] = raw
		persisted, err = a.writer.client.CoreV1().Secrets(a.namespace).Update(ctx, secret, metav1.UpdateOptions{})
	}
	if err != nil {
		return fmt.Errorf("persist agent key Secret: %w", err)
	}
	stored, _, err := a.decode(persisted)
	if err != nil {
		return err
	}
	if !bytes.Equal(persisted.Data[secretDataKey], raw) {
		return fmt.Errorf("persisted agent key document was unexpectedly modified")
	}
	return a.apply(stored)
}
