package keys

import (
	"errors"
	"testing"
	"time"

	"github.com/karlrissland/homelab-api2mcp/internal/manifest"
)

func TestStoreMintLookupRevoke(t *testing.T) {
	now := time.Date(2026, time.September, 30, 17, 30, 0, 0, time.UTC)
	store := newStore(func() time.Time { return now })

	key, err := store.Mint("hermes-alice", manifest.TierAdmin)
	if err != nil {
		t.Fatalf("Mint() error = %v", err)
	}
	if key == "" {
		t.Fatal("Mint() returned an empty key")
	}

	record, ok := store.Lookup(key)
	if !ok {
		t.Fatal("Lookup() returned not found for minted key")
	}
	if record.AgentInstance != "hermes-alice" {
		t.Fatalf("Lookup().AgentInstance = %q, want %q", record.AgentInstance, "hermes-alice")
	}
	if record.Tier != manifest.TierAdmin {
		t.Fatalf("Lookup().Tier = %q, want %q", record.Tier, manifest.TierAdmin)
	}
	if !record.CreatedAt.Equal(now) {
		t.Fatalf("Lookup().CreatedAt = %v, want %v", record.CreatedAt, now)
	}

	if err := store.Revoke(key); err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}
	if _, ok := store.Lookup(key); ok {
		t.Fatal("Lookup() succeeded after Revoke()")
	}
}

func TestStoreList(t *testing.T) {
	now := time.Date(2026, time.September, 30, 17, 30, 0, 0, time.UTC)
	store := newStore(func() time.Time { return now })

	first, err := store.Mint("hermes-alice", manifest.TierUser)
	if err != nil {
		t.Fatalf("Mint(first) error = %v", err)
	}
	if first == "" {
		t.Fatal("Mint(first) returned empty key")
	}

	store.now = func() time.Time { return now.Add(time.Minute) }
	second, err := store.Mint("hermes-bob", manifest.TierAdmin)
	if err != nil {
		t.Fatalf("Mint(second) error = %v", err)
	}
	if second == "" {
		t.Fatal("Mint(second) returned empty key")
	}

	got := store.List()
	if len(got) != 2 {
		t.Fatalf("List() returned %d records, want 2", len(got))
	}
	if got[0].AgentInstance != "hermes-alice" || got[0].Tier != manifest.TierUser {
		t.Fatalf("List()[0] = %+v, want hermes-alice user", got[0])
	}
	if got[1].AgentInstance != "hermes-bob" || got[1].Tier != manifest.TierAdmin {
		t.Fatalf("List()[1] = %+v, want hermes-bob admin", got[1])
	}
}

func TestStoreMintValidation(t *testing.T) {
	t.Parallel()

	store := NewStore()
	cases := []struct {
		name          string
		agentInstance string
		tier          manifest.Tier
	}{
		{name: "missing agent instance", tier: manifest.TierUser},
		{name: "invalid tier", agentInstance: "hermes-alice", tier: manifest.Tier("superadmin")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			key, err := store.Mint(tc.agentInstance, tc.tier)
			if err == nil {
				t.Fatal("Mint() error = nil, want error")
			}
			if key != "" {
				t.Fatalf("Mint() key = %q, want empty", key)
			}
		})
	}
}

func TestStoreRevokeMissingKey(t *testing.T) {
	t.Parallel()

	store := NewStore()
	err := store.Revoke("missing")
	if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("Revoke() error = %v, want ErrKeyNotFound", err)
	}
}
