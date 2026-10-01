// Package upstreamcreds resolves per-tool upstream credentials (the
// values Liquid templates and the passthrough relay inject into
// backend/upstream MCP calls) from two well-known Secrets per app
// instance.
//
// Per the resolved design (homelab-api2mcp#1), this is a full in-memory
// preload: Reload rebuilds the entire cache from the currently known app
// list, and lookups never touch the Kubernetes API directly. This keeps
// per-call latency low and only requires `get` RBAC on Secrets in
// mcp2rest's own namespace (the app-instance names are already known
// from discovery, so no `list`/`watch` on Secrets is needed). A missing
// Secret is not an error — it fails open to an empty credential, and the
// backend's own auth rejection becomes the caller-visible error.
package upstreamcreds

import (
	"context"
	"fmt"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/karlrissland/homelab-api2mcp/internal/manifest"
)

// APIKeysSecretName returns the Secret name holding rendered/REST tool
// credentials for one app instance.
func APIKeysSecretName(appInstance string) string {
	return fmt.Sprintf("%s-api-keys", appInstance)
}

// MCPKeysSecretName returns the Secret name holding passthrough tool
// credentials for one app instance.
func MCPKeysSecretName(appInstance string) string {
	return fmt.Sprintf("%s-mcp-keys", appInstance)
}

// Cache is an in-memory, preload-and-manual-reload store of upstream
// credential values, keyed by app instance name and credential key.
type Cache struct {
	client    kubernetes.Interface
	namespace string

	mu  sync.RWMutex
	api map[string]map[string]string
	mcp map[string]map[string]string
}

// New builds a Cache that reads Secrets from the provided namespace
// (mcp2rest's own namespace — credentials live alongside the proxy, not
// in each app's namespace).
func New(client kubernetes.Interface, namespace string) (*Cache, error) {
	if client == nil {
		return nil, fmt.Errorf("new upstream credential cache: kubernetes client is required")
	}
	if namespace == "" {
		return nil, fmt.Errorf("new upstream credential cache: namespace is required")
	}
	return &Cache{
		client:    client,
		namespace: namespace,
		api:       map[string]map[string]string{},
		mcp:       map[string]map[string]string{},
	}, nil
}

// Reload rebuilds the cache from scratch against the provided list of
// currently known apps, dropping any app no longer present. A missing
// Secret for a given app yields an empty credential map for that app
// (fail-open), not an error.
func (c *Cache) Reload(ctx context.Context, apps []*manifest.App) error {
	api := make(map[string]map[string]string, len(apps))
	mcp := make(map[string]map[string]string, len(apps))

	for _, app := range apps {
		if app == nil || app.Name == "" {
			continue
		}
		apiData, err := c.readSecret(ctx, APIKeysSecretName(app.Name))
		if err != nil {
			return fmt.Errorf("reload upstream credentials for app %q: %w", app.Name, err)
		}
		mcpData, err := c.readSecret(ctx, MCPKeysSecretName(app.Name))
		if err != nil {
			return fmt.Errorf("reload upstream credentials for app %q: %w", app.Name, err)
		}
		api[app.Name] = apiData
		mcp[app.Name] = mcpData
	}

	c.mu.Lock()
	c.api = api
	c.mcp = mcp
	c.mu.Unlock()

	return nil
}

func (c *Cache) readSecret(ctx context.Context, name string) (map[string]string, error) {
	secret, err := c.client.CoreV1().Secrets(c.namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("get secret %q in namespace %q: %w", name, c.namespace, err)
	}

	data := make(map[string]string, len(secret.Data))
	for key, value := range secret.Data {
		data[key] = string(value)
	}
	return data, nil
}

// Credential returns the resolved upstream credential value for an app
// instance + credential key, scoped by tool type (rendered tools read
// from the api-keys Secret, passthrough tools read from the mcp-keys
// Secret). Returns "" when the key is empty, the app is unknown, or the
// credential is not present — callers should treat an empty result as
// "no credential available" and let the backend's own auth rejection
// surface as the error, not treat it specially here.
func (c *Cache) Credential(toolType manifest.ToolType, appInstance, key string) string {
	if key == "" {
		return ""
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	var bucket map[string]map[string]string
	switch toolType {
	case manifest.ToolTypeRendered:
		bucket = c.api
	case manifest.ToolTypePassthrough:
		bucket = c.mcp
	default:
		return ""
	}

	return bucket[appInstance][key]
}
