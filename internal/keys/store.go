package keys

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
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
	mu       sync.RWMutex
	records  map[[sha256.Size]byte]Record
	now      func() time.Time
	encoding *base64.Encoding
}

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

	buf := make([]byte, keyBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("mint key for agent instance %q: read random bytes: %w", agentInstance, err)
	}

	key := s.encoding.EncodeToString(buf)
	record := Record{
		AgentInstance: agentInstance,
		Tier:          tier,
		CreatedAt:     s.now().UTC(),
	}

	s.mu.Lock()
	s.records[digestKey(key)] = record
	s.mu.Unlock()

	return key, nil
}

// Lookup returns the metadata associated with a raw API key.
func (s *Store) Lookup(key string) (Record, bool) {
	if key == "" {
		return Record{}, false
	}

	s.mu.RLock()
	record, ok := s.records[digestKey(key)]
	s.mu.RUnlock()

	return record, ok
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
