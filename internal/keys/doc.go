// Package keys persists namespace-qualified agent API keys and authorization
// metadata in gateway-namespace Secrets and restores their verification index
// at startup. It also creates or restores the persistent bootstrap credential.
package keys
