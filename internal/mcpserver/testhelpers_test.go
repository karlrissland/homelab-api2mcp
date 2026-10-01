package mcpserver

import (
	"testing"

	"k8s.io/client-go/kubernetes"

	"github.com/karlrissland/homelab-api2mcp/internal/upstreamcreds"
)

// mustCreds builds an empty upstream credential cache for tests that don't
// exercise credential resolution directly.
func mustCreds(t *testing.T, client kubernetes.Interface) *upstreamcreds.Cache {
	t.Helper()
	creds, err := upstreamcreds.New(client, "mcp2rest")
	if err != nil {
		t.Fatalf("upstreamcreds.New() error = %v", err)
	}
	return creds
}
