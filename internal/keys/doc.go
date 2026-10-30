// Package keys persists namespace-qualified agent API keys and authorization
// metadata in gateway-namespace Secrets and restores their verification index
// at startup. It also creates or restores the persistent bootstrap credential.
// Keys identify an owning agent namespace, instance, and tier, not an app.
// AgentKeys owns authoritative persistence; SecretWriter also retains legacy
// raw-key delivery helpers. Active gateway verification caches are not
// automatically synchronized across replicas.
package keys
