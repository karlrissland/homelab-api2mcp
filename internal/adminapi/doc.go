// Package adminapi implements mcp2rest's reserved management MCP tools.
// Every registered management call requires a gateway admin-tier key; tool
// discovery itself is not authorization. App registrations and durable agent
// keys are managed here, while package keys owns their credential persistence
// and initializes the persistent bootstrap credential at startup.
package adminapi
