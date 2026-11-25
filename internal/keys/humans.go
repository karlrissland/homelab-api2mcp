package keys

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/karlrissland/homelab-api2mcp/internal/manifest"
)

// HumanSecretLabel selects canonical-human records in the gateway namespace.
const HumanSecretLabel = "mcp2rest.homelab.dev/human-keys"

// HumanState describes authoritative human eligibility.
type HumanState string

const (
	// HumanActive allows keys at the attested tiers.
	HumanActive HumanState = "active"
	// HumanDisabled withdraws all keys until explicit reactivation and issuance.
	HumanDisabled HumanState = "disabled"
	// HumanDeleted is a terminal tombstone for a canonical identity.
	HumanDeleted HumanState = "deleted"
)

// Human describes an administrative assertion from hlctl's authoritative user model.
type Human struct {
	HomelabUserID  string     `json:"homelabUserId"`
	Source         string     `json:"source"`
	SourceRevision string     `json:"sourceRevision"`
	State          HumanState `json:"state"`
	Admin          bool       `json:"admin"`
	Revision       int64      `json:"revision"`
}

// HumanCredential is protected raw key material for exactly one human and tier.
type HumanCredential struct {
	HomelabUserID string        `json:"homelabUserId"`
	Tier          manifest.Tier `json:"tier"`
	Revision      int64         `json:"revision"`
	CreatedAt     time.Time     `json:"createdAt"`
	Key           string        `json:"key"`
}

// HumanError carries a stable, non-secret provider failure code.
type HumanError struct {
	Code string `json:"code"`
}

// Error returns only the fixed code, never storage or decoder diagnostics.
func (e *HumanError) Error() string { return "human credentials: " + e.Code }

func humanError(code string) error { return &HumanError{Code: code} }

type humanDocument struct {
	Version int        `json:"version"`
	Human   Human      `json:"human"`
	Keys    []agentKey `json:"keys"`
}

type humanSecretPersistence interface {
	listHumanSecrets(context.Context, string) ([]corev1.Secret, error)
	saveHumanSecret(context.Context, *corev1.Secret, bool) (*corev1.Secret, error)
}

// HumanKeys owns canonical-human persistence and verification independently of agents.
// One active gateway is required. Administrative reads reconcile the verifier but
// never write Secrets; unreadable state withdraws human verification until recovery.
type HumanKeys struct {
	mu        sync.Mutex
	writer    humanSecretPersistence
	namespace string
	store     *Store
}

// NewHumanKeys constructs a repository using the existing Secret persistence adapter.
func NewHumanKeys(writer *SecretWriter, namespace string, store *Store) (*HumanKeys, error) {
	if writer == nil || store == nil || strings.TrimSpace(namespace) == "" {
		return nil, humanError("invalid_arguments")
	}
	return &HumanKeys{writer: writer, namespace: namespace, store: store}, nil
}

func humanSecretName(id string) string {
	digest := sha256.Sum256([]byte(id))
	return "mcp2rest-human-" + hex.EncodeToString(digest[:])
}

func validOpaqueID(id string, limit int) bool {
	return len(id) <= limit && utf8.ValidString(id) && strings.TrimSpace(id) != ""
}

func validateHuman(h Human) error {
	if !validOpaqueID(h.HomelabUserID, 4096) || h.Source != "hlctl" || !validOpaqueID(h.SourceRevision, 1024) ||
		(h.State != HumanActive && h.State != HumanDisabled && h.State != HumanDeleted) ||
		(h.State != HumanActive && h.Admin) {
		return humanError("invalid_arguments")
	}
	return nil
}

// Restore validates all human records and reconciles verification before serving.
func (h *HumanKeys) Restore(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := h.snapshot(ctx)
	return err
}

// Get returns non-secret authoritative identity metadata without persistent writes.
func (h *HumanKeys) Get(ctx context.Context, id string) (Human, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !validOpaqueID(id, 4096) {
		return Human{}, humanError("invalid_arguments")
	}
	documents, err := h.snapshot(ctx)
	if err != nil {
		return Human{}, err
	}
	secret, ok := documents[id]
	if !ok {
		return Human{}, humanError("human_not_found")
	}
	document, err := h.decode(secret)
	return document.Human, err
}

// Put explicitly attests a source snapshot with optimistic identity revision.
// Deactivation and role loss discard withdrawn keys; deletion is terminal.
func (h *HumanKeys) Put(ctx context.Context, desired Human, expectedRevision int64) (Human, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := validateHuman(desired); err != nil {
		return Human{}, err
	}
	if expectedRevision < 0 || desired.Revision != 0 {
		return Human{}, humanError("invalid_arguments")
	}
	documents, err := h.snapshot(ctx)
	if err != nil {
		return Human{}, err
	}
	secret := documents[desired.HomelabUserID]
	document := humanDocument{Version: 1, Keys: []agentKey{}}
	if secret != nil {
		document, err = h.decode(secret)
		if err != nil {
			return Human{}, err
		}
	}
	if document.Human.Revision != expectedRevision {
		return Human{}, humanError("revision_conflict")
	}
	if document.Human.State == HumanDeleted && desired.State != HumanDeleted {
		return Human{}, humanError("human_deleted")
	}
	desired.Revision = document.Human.Revision
	if desired == document.Human {
		return desired, nil
	}
	if desired.Revision == 1<<63-1 {
		return Human{}, humanError("revision_conflict")
	}
	desired.Revision++
	document.Human = desired
	kept := make([]agentKey, 0, len(document.Keys))
	for _, entry := range document.Keys {
		if desired.State == HumanActive && (entry.Tier == manifest.TierUser || desired.Admin) {
			kept = append(kept, entry)
		}
	}
	document.Keys = kept
	if err := h.save(ctx, secret, document); err != nil {
		return Human{}, err
	}
	return desired, nil
}

// Create explicitly issues one missing eligible tier, never rotating or ensuring.
func (h *HumanKeys) Create(ctx context.Context, id string, tier manifest.Tier, expectedRevision int64) (HumanCredential, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	secret, document, err := h.loadEligible(ctx, id, tier, expectedRevision)
	if err != nil {
		return HumanCredential{}, err
	}
	for _, entry := range document.Keys {
		if entry.Tier == tier {
			return HumanCredential{}, humanError("key_exists")
		}
	}
	key, err := h.store.generateKey()
	if err != nil {
		return HumanCredential{}, humanError("persistence_failed")
	}
	if _, exists := h.store.LookupStored(key); exists {
		return HumanCredential{}, humanError("duplicate_record")
	}
	entry := agentKey{Key: key, Tier: tier, CreatedAt: h.store.now().UTC()}
	document.Keys = append(document.Keys, entry)
	if err := h.save(ctx, secret, document); err != nil {
		return HumanCredential{}, err
	}
	return humanCredential(document.Human, entry), nil
}

// GetKey returns exactly one existing eligible key, without issuance or writes.
func (h *HumanKeys) GetKey(ctx context.Context, id string, tier manifest.Tier, expectedRevision int64) (HumanCredential, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, document, err := h.loadEligible(ctx, id, tier, expectedRevision)
	if err != nil {
		return HumanCredential{}, err
	}
	for _, entry := range document.Keys {
		if entry.Tier == tier {
			return humanCredential(document.Human, entry), nil
		}
	}
	return HumanCredential{}, humanError("key_not_found")
}

func humanCredential(h Human, entry agentKey) HumanCredential {
	return HumanCredential{HomelabUserID: h.HomelabUserID, Tier: entry.Tier, Revision: h.Revision, CreatedAt: entry.CreatedAt, Key: entry.Key}
}

func (h *HumanKeys) loadEligible(ctx context.Context, id string, tier manifest.Tier, revision int64) (*corev1.Secret, humanDocument, error) {
	if !validOpaqueID(id, 4096) || !tier.Valid() || revision < 1 {
		return nil, humanDocument{}, humanError("invalid_arguments")
	}
	documents, err := h.snapshot(ctx)
	if err != nil {
		return nil, humanDocument{}, err
	}
	secret := documents[id]
	if secret == nil {
		return nil, humanDocument{}, humanError("human_not_found")
	}
	document, err := h.decode(secret)
	if err != nil {
		return nil, humanDocument{}, err
	}
	switch {
	case document.Human.State == HumanDeleted:
		err = humanError("human_deleted")
	case document.Human.State == HumanDisabled:
		err = humanError("human_disabled")
	case tier == manifest.TierAdmin && !document.Human.Admin:
		err = humanError("tier_ineligible")
	case document.Human.Revision != revision:
		err = humanError("revision_conflict")
	}
	return secret, document, err
}

// snapshot rejects duplicate identities even when a second Secret uses a
// noncanonical name. A collection read is necessary; named Get alone misses it.
func (h *HumanKeys) snapshot(ctx context.Context) (map[string]*corev1.Secret, error) {
	secrets, err := h.writer.listHumanSecrets(ctx, h.namespace)
	if err != nil {
		return nil, h.withdraw(humanError("storage_unavailable"))
	}
	documents := make(map[string]*corev1.Secret, len(secrets))
	records := make(map[[sha256.Size]byte]Record)
	for i := range secrets {
		secret := &secrets[i]
		document, err := h.decode(secret)
		if err != nil {
			return nil, h.withdraw(err)
		}
		id := document.Human.HomelabUserID
		if _, duplicate := documents[id]; duplicate {
			return nil, h.withdraw(humanError("duplicate_record"))
		}
		documents[id] = secret
		if secret.Name != humanSecretName(id) {
			// Defer name validation until duplicate identity detection is complete.
			continue
		}
		for _, entry := range document.Keys {
			digest := digestKey(entry.Key)
			if _, duplicate := records[digest]; duplicate {
				return nil, h.withdraw(humanError("duplicate_record"))
			}
			records[digest] = Record{HomelabUserID: id, Tier: entry.Tier, CreatedAt: entry.CreatedAt}
		}
	}
	for id, secret := range documents {
		if secret.Name != humanSecretName(id) {
			return nil, h.withdraw(humanError("invalid_record"))
		}
	}
	if err := h.store.replaceHumans(records); err != nil {
		return nil, h.withdraw(humanError("duplicate_record"))
	}
	return documents, nil
}

func (h *HumanKeys) withdraw(err error) error {
	// Removing only human records cannot conflict with another owner.
	if clearErr := h.store.replaceHumans(nil); clearErr != nil {
		return humanError("invalid_record")
	}
	return err
}

func (h *HumanKeys) decode(secret *corev1.Secret) (humanDocument, error) {
	var document humanDocument
	if secret.Namespace != h.namespace || secret.Type != corev1.SecretTypeOpaque ||
		secret.Labels[HumanSecretLabel] != "true" || secret.Labels[AgentSecretLabel] != "" ||
		len(secret.OwnerReferences) != 0 {
		return document, humanError("invalid_record")
	}
	raw := secret.Data[secretDataKey]
	if err := uniqueJSONFields(json.NewDecoder(bytes.NewReader(raw)), 0); err != nil {
		return document, humanError("invalid_record")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return document, humanError("invalid_record")
	}
	if decoder.Decode(new(any)) != io.EOF || document.Version != 1 || document.Human.Revision < 1 ||
		validateHuman(document.Human) != nil || document.Keys == nil {
		return document, humanError("invalid_record")
	}
	tiers := make(map[manifest.Tier]bool, 2)
	digests := make(map[[sha256.Size]byte]bool, 2)
	for _, entry := range document.Keys {
		if tiers[entry.Tier] || digests[digestKey(entry.Key)] {
			return document, humanError("duplicate_record")
		}
		tiers[entry.Tier], digests[digestKey(entry.Key)] = true, true
		record := Record{HomelabUserID: document.Human.HomelabUserID, Tier: entry.Tier, CreatedAt: entry.CreatedAt}
		if h.store.validate(entry.Key, record) != nil || document.Human.State != HumanActive ||
			(entry.Tier == manifest.TierAdmin && !document.Human.Admin) {
			return document, humanError("invalid_record")
		}
	}
	return document, nil
}

func (h *HumanKeys) save(ctx context.Context, secret *corev1.Secret, document humanDocument) error {
	raw, err := json.Marshal(document)
	if err != nil {
		return h.withdraw(humanError("persistence_failed"))
	}
	create := secret == nil
	if create {
		secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: humanSecretName(document.Human.HomelabUserID), Namespace: h.namespace, Labels: map[string]string{HumanSecretLabel: "true"}},
			Type:       corev1.SecretTypeOpaque,
			Data:       map[string][]byte{secretDataKey: raw},
		}
	} else {
		secret.Data[secretDataKey] = raw
	}
	secret, err = h.writer.saveHumanSecret(ctx, secret, create)
	if apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err) {
		return h.withdraw(humanError("revision_conflict"))
	}
	if err != nil {
		return h.withdraw(humanError("persistence_failed"))
	}
	if secret == nil || !bytes.Equal(secret.Data[secretDataKey], raw) {
		return h.withdraw(humanError("persistence_failed"))
	}
	if _, err := h.decode(secret); err != nil {
		return h.withdraw(err)
	}
	documents, err := h.snapshot(ctx)
	if err != nil {
		return err
	}
	current := documents[document.Human.HomelabUserID]
	if current == nil || !bytes.Equal(current.Data[secretDataKey], raw) {
		return h.withdraw(humanError("persistence_failed"))
	}
	return nil
}

// uniqueJSONFields rejects ambiguous JSON objects before normal typed decoding.
func uniqueJSONFields(decoder *json.Decoder, depth int) error {
	if depth > 16 {
		return errors.New("invalid JSON depth")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	fields := make(map[string]bool)
	for decoder.More() {
		if delim == '{' {
			token, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := token.(string)
			if !ok || fields[name] {
				return errors.New("duplicate JSON field")
			}
			fields[name] = true
		}
		if err := uniqueJSONFields(decoder, depth+1); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}
