package keys

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/karlrissland/homelab-api2mcp/internal/manifest"
)

const keyBytes = 32

// ErrKeyNotFound reports that a key was not present in the in-memory store.
var ErrKeyNotFound = errors.New("key not found")

// Record describes one minted key without retaining the raw key material.
type Record struct {
	AgentInstance string
	Tier          manifest.Tier
	CreatedAt     time.Time
}

// Store is an in-memory key store keyed by the SHA-256 digest of the raw
// API key. The raw key is returned once from Mint and is never retained.
type Store struct {
	mu          sync.RWMutex
	records     map[[sha256.Size]byte]Record
	now         func() time.Time
	encoding    *base64.Encoding
	disableAuth bool
}

// insecureAuthDisabledAgentInstance is the synthetic AgentInstance on the
// record DisableAuth's Lookup bypass always returns.
const insecureAuthDisabledAgentInstance = "auth-disabled"

// NewStore builds an empty in-memory key store.
func NewStore() *Store {
	return newStore(time.Now)
}

func newStore(now func() time.Time) *Store {
	return &Store{
		records:  make(map[[sha256.Size]byte]Record),
		now:      now,
		encoding: base64.RawURLEncoding,
	}
}

// Mint generates a new API key for the given agent instance and tier,
// stores its SHA-256 digest in memory, and returns the raw key once.
func (s *Store) Mint(agentInstance string, tier manifest.Tier) (string, error) {
	if agentInstance == "" {
		return "", fmt.Errorf("mint key: agent instance is required")
	}
	if !tier.Valid() {
		return "", fmt.Errorf("mint key for agent instance %q: invalid tier %q", agentInstance, tier)
	}

	key, err := s.generateKey()
	if err != nil {
		return "", fmt.Errorf("mint key for agent instance %q: %w", agentInstance, err)
	}
	record := Record{
		AgentInstance: agentInstance,
		Tier:          tier,
		CreatedAt:     s.now().UTC(),
	}

	if err := s.Import(key, record); err != nil {
		return "", fmt.Errorf("mint key for agent instance %q: %w", agentInstance, err)
	}
	return key, nil
}

// Import restores verification state for a persisted key without retaining its
// raw value. Reimporting the same key and identity preserves its existing record.
func (s *Store) Import(key string, record Record) error {
	if record.AgentInstance == "" {
		return fmt.Errorf("import key: agent instance is required")
	}
	if !record.Tier.Valid() {
		return fmt.Errorf("import key: invalid tier %q", record.Tier)
	}
	if record.CreatedAt.IsZero() {
		return fmt.Errorf("import key: creation time is required")
	}
	decoded, err := s.encoding.Strict().DecodeString(key)
	if err != nil || len(decoded) != keyBytes || s.encoding.EncodeToString(decoded) != key {
		return fmt.Errorf("import key: expected a canonical base64url-encoded 32-byte key")
	}
	digest := digestKey(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.records[digest]; ok {
		if existing.AgentInstance != record.AgentInstance || existing.Tier != record.Tier {
			return fmt.Errorf("import key: key is already registered for a different identity or tier")
		}
		return nil
	}
	s.records[digest] = record
	return nil
}

func (s *Store) generateKey() (string, error) {
	buf := make([]byte, keyBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read random key bytes: %w", err)
	}
	return s.encoding.EncodeToString(buf), nil
}

// DisableAuth puts the store into an insecure "auth disabled" mode: every
// Lookup call succeeds with a synthetic admin-tier record, regardless of
// the key (or lack of a key) presented. This is a temporary operational
// escape hatch (env var MCP2REST_DISABLE_AUTH, see cmd/mcp2rest/main.go)
// to reduce moving parts while debugging unrelated issues -- it disables
// authn/authz cluster-wide for every app and every management/skills
// tool proxied through this mcp2rest instance. Not intended to be left on
// in production; re-enable by unsetting the env var and restarting.
func (s *Store) DisableAuth() {
	s.mu.Lock()
	s.disableAuth = true
	s.mu.Unlock()
}

// Lookup returns the metadata associated with a raw API key.
func (s *Store) Lookup(key string) (Record, bool) {
	s.mu.RLock()
	disabled := s.disableAuth
	s.mu.RUnlock()
	if disabled {
		return Record{
			AgentInstance: insecureAuthDisabledAgentInstance,
			Tier:          manifest.TierAdmin,
			CreatedAt:     s.now().UTC(),
		}, true
	}

	if key == "" {
		return Record{}, false
	}

	s.mu.RLock()
	record, ok := s.records[digestKey(key)]
	s.mu.RUnlock()

	return record, ok
}

// List returns a snapshot of all known key records in creation order.
func (s *Store) List() []Record {
	s.mu.RLock()
	defer s.mu.RUnlock()

	records := make([]Record, 0, len(s.records))
	for _, record := range s.records {
		records = append(records, record)
	}
	slices.SortFunc(records, func(a, b Record) int {
		switch {
		case a.CreatedAt.Before(b.CreatedAt):
			return -1
		case a.CreatedAt.After(b.CreatedAt):
			return 1
		case a.AgentInstance < b.AgentInstance:
			return -1
		case a.AgentInstance > b.AgentInstance:
			return 1
		case a.Tier < b.Tier:
			return -1
		case a.Tier > b.Tier:
			return 1
		default:
			return 0
		}
	})

	return records
}

// Revoke removes a raw API key from the in-memory store.
func (s *Store) Revoke(key string) error {
	if key == "" {
		return fmt.Errorf("revoke key: key is required")
	}

	digest := digestKey(key)

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.records[digest]; !ok {
		return fmt.Errorf("revoke key: %w", ErrKeyNotFound)
	}
	delete(s.records, digest)

	return nil
}

func digestKey(key string) [sha256.Size]byte {
	return sha256.Sum256([]byte(key))
}
