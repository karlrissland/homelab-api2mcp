// Command mcp2rest starts the mcp2rest MCP (Model Context Protocol)
// Streamable HTTP server. See docs/decisions/mcp2rest-plan.md for the
// full architecture; this entrypoint only parses configuration and wires
// dependencies together — it holds no business logic itself.
package main

import (
	"log"
	"net/http"
	"os"

	"github.com/karlrissland/homelab-api2mcp/internal/mcpserver"
)

// version is set at release time via -ldflags; "dev" is the default for
// local builds.
var version = "dev"

func main() {
	addr := os.Getenv("MCP2REST_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	log.Printf("mcp2rest %s starting on %s", version, addr)

	server := mcpserver.New()
	handler := mcpserver.NewHandler(server)

	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatalf("mcp2rest: server failed: %v", err)
	}
}
