// Package keys mints per-agent-instance API keys, keeps an in-memory
// lookup index for them, and writes issued keys into per-instance
// Kubernetes Secrets for later consumption by agent pods.
package keys
