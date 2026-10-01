// Command mcp2rest starts the mcp2rest MCP (Model Context Protocol)
// Streamable HTTP server. See docs/decisions/mcp2rest-plan.md for the
// full architecture; this entrypoint only parses configuration and wires
// dependencies together — it holds no business logic itself.
package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/karlrissland/homelab-api2mcp/internal/adminapi"
	"github.com/karlrissland/homelab-api2mcp/internal/discovery"
	"github.com/karlrissland/homelab-api2mcp/internal/keys"
	"github.com/karlrissland/homelab-api2mcp/internal/mcpserver"
	"github.com/karlrissland/homelab-api2mcp/internal/render"
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

	cfg := mustClusterConfig()
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Fatalf("mcp2rest: create kubernetes client: %v", err)
	}
	skillClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		log.Fatalf("mcp2rest: create dynamic skill client: %v", err)
	}

	table, err := discovery.New(client)
	if err != nil {
		log.Fatalf("mcp2rest: create discovery table: %v", err)
	}
	ctx := context.Background()
	go func() {
		if err := table.Run(ctx); err != nil {
			log.Fatalf("mcp2rest: discovery table stopped: %v", err)
		}
	}()

	store := keys.NewStore()
	writer, err := keys.NewSecretWriter(client)
	if err != nil {
		log.Fatalf("mcp2rest: create secret writer: %v", err)
	}
	admin, err := adminapi.New(client, table, store, writer)
	if err != nil {
		log.Fatalf("mcp2rest: create admin api: %v", err)
	}
	if _, _, err := admin.EnsureBootstrapAdminKey(os.Stderr); err != nil {
		log.Fatalf("mcp2rest: bootstrap admin key: %v", err)
	}

	handler, err := mcpserver.NewRuntimeHandler(table, store, render.New(http.DefaultClient), admin, skillClient)
	if err != nil {
		log.Fatalf("mcp2rest: create runtime handler: %v", err)
	}

	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatalf("mcp2rest: server failed: %v", err)
	}
}

func mustClusterConfig() *rest.Config {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		log.Fatalf("mcp2rest: load in-cluster kubernetes config: %v", err)
	}
	return cfg
}
