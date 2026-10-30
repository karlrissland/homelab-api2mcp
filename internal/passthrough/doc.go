// Package passthrough supports two upstream MCP paths. Relay calls individually
// declared tools using the official Streamable HTTP client and flattens results
// to text. Proxy forwards an entire native MCP HTTP endpoint without interpreting
// protocol bodies, replacing client authorization with server-owned credentials.
// Gateway authentication, and tier checks for declared tools, belong to callers.
package passthrough
