package upstreamcreds

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/karlrissland/homelab-api2mcp/internal/manifest"
)

func TestNewRequiresDependencies(t *testing.T) {
	if _, err := New(nil, "mcp2rest"); err == nil {
		t.Error("expected error for nil client, got nil")
	}
	if _, err := New(fake.NewSimpleClientset(), ""); err == nil {
		t.Error("expected error for empty namespace, got nil")
	}
}

func TestSecretNameHelpers(t *testing.T) {
	if got, want := APIKeysSecretName("gitea"), "gitea-api-keys"; got != want {
		t.Errorf("APIKeysSecretName() = %q, want %q", got, want)
	}
	if got, want := MCPKeysSecretName("gitea"), "gitea-mcp-keys"; got != want {
		t.Errorf("MCPKeysSecretName() = %q, want %q", got, want)
	}
}

func TestReloadAndCredentialLookup(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "gitea-api-keys", Namespace: "mcp2rest"},
			Data:       map[string][]byte{"read-token": []byte("api-value")},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "gitea-mcp-keys", Namespace: "mcp2rest"},
			Data:       map[string][]byte{"mcp-token": []byte("mcp-value")},
		},
	)
	cache, err := New(client, "mcp2rest")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	apps := []*manifest.App{{Name: "gitea"}, {Name: "metube"}}
	if err := cache.Reload(context.Background(), apps); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}

	if got := cache.Credential(manifest.ToolTypeRendered, "gitea", "read-token"); got != "api-value" {
		t.Errorf("Credential(rendered) = %q, want %q", got, "api-value")
	}
	if got := cache.Credential(manifest.ToolTypePassthrough, "gitea", "mcp-token"); got != "mcp-value" {
		t.Errorf("Credential(passthrough) = %q, want %q", got, "mcp-value")
	}

	// Fail-open: unprovisioned app, unknown key, empty key, and wrong tool
	// type all resolve to "" rather than an error.
	if got := cache.Credential(manifest.ToolTypeRendered, "metube", "anything"); got != "" {
		t.Errorf("Credential() for unprovisioned app = %q, want empty", got)
	}
	if got := cache.Credential(manifest.ToolTypeRendered, "gitea", "missing-key"); got != "" {
		t.Errorf("Credential() for unknown key = %q, want empty", got)
	}
	if got := cache.Credential(manifest.ToolTypeRendered, "gitea", ""); got != "" {
		t.Errorf("Credential() for empty key = %q, want empty", got)
	}
	if got := cache.Credential(manifest.ToolTypePassthrough, "gitea", "read-token"); got != "" {
		t.Errorf("Credential() reading api-keys value via passthrough type = %q, want empty", got)
	}
}

func TestReloadDropsDeregisteredApps(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "gitea-api-keys", Namespace: "mcp2rest"},
		Data:       map[string][]byte{"read-token": []byte("api-value")},
	})
	cache, err := New(client, "mcp2rest")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if err := cache.Reload(context.Background(), []*manifest.App{{Name: "gitea"}}); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	if got := cache.Credential(manifest.ToolTypeRendered, "gitea", "read-token"); got != "api-value" {
		t.Fatalf("Credential() before deregister = %q, want %q", got, "api-value")
	}

	if err := cache.Reload(context.Background(), []*manifest.App{{Name: "metube"}}); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	if got := cache.Credential(manifest.ToolTypeRendered, "gitea", "read-token"); got != "" {
		t.Errorf("Credential() after deregister = %q, want empty (dropped from cache)", got)
	}
}
