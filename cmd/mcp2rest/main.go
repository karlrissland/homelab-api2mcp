// Command mcp2rest starts the mcp2rest MCP (Model Context Protocol)
// Streamable HTTP server. See docs/decisions/mcp2rest-plan.md for the
// full architecture; this entrypoint only parses configuration and wires
// dependencies together — it holds no business logic itself.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/karlrissland/homelab-api2mcp/internal/adminapi"
	"github.com/karlrissland/homelab-api2mcp/internal/discovery"
	"github.com/karlrissland/homelab-api2mcp/internal/keys"
	"github.com/karlrissland/homelab-api2mcp/internal/mcpserver"
	"github.com/karlrissland/homelab-api2mcp/internal/passthrough"
	"github.com/karlrissland/homelab-api2mcp/internal/pipeline"
	"github.com/karlrissland/homelab-api2mcp/internal/render"
	"github.com/karlrissland/homelab-api2mcp/internal/upstreamcreds"
)

// version is set at release time via -ldflags; "dev" is the default for
// local builds.
var version = "dev"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	addr := os.Getenv("MCP2REST_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	namespace := os.Getenv("MCP2REST_NAMESPACE")
	if namespace == "" {
		namespace = "mcp2rest"
	}

	logger.Info("mcp2rest starting", "version", version, "addr", addr)

	cfg := mustClusterConfig()
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		fatal("create kubernetes client", err)
	}
	skillClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		fatal("create dynamic skill client", err)
	}

	table, err := discovery.New(client)
	if err != nil {
		fatal("create discovery table", err)
	}
	ctx := context.Background()
	go func() {
		if err := table.Run(ctx); err != nil {
			fatal("discovery table stopped", err)
		}
	}()

	store := keys.NewStore()
	writer, err := keys.NewSecretWriter(client)
	if err != nil {
		fatal("create secret writer", err)
	}
	creds, err := upstreamcreds.New(client, namespace)
	if err != nil {
		fatal("create upstream credential cache", err)
	}
	if err := creds.Reload(ctx, table.List()); err != nil {
		fatal("initial upstream credential reload", err)
	}
	admin, err := adminapi.New(client, table, store, writer, creds)
	if err != nil {
		fatal("create admin api", err)
	}
	if _, _, err := admin.EnsureBootstrapAdminKey(os.Stderr); err != nil {
		fatal("bootstrap admin key", err)
	}

	registry := prometheus.NewRegistry()
	if err := registry.Register(collectors.NewGoCollector()); err != nil {
		fatal("register go collector", err)
	}
	if err := registry.Register(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{})); err != nil {
		fatal("register process collector", err)
	}
	metrics, err := pipeline.NewMetrics(registry)
	if err != nil {
		fatal("create metrics", err)
	}
	metricsHandler := promhttp.HandlerFor(registry, promhttp.HandlerOpts{})

	handler, err := mcpserver.NewRuntimeHandler(
		table,
		store,
		render.New(http.DefaultClient),
		passthrough.New(http.DefaultClient),
		creds,
		admin,
		skillClient,
		metrics,
		logger,
		metricsHandler,
	)
	if err != nil {
		fatal("create runtime handler", err)
	}

	if err := http.ListenAndServe(addr, handler); err != nil {
		fatal("server failed", err)
	}
}

func mustClusterConfig() *rest.Config {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		fatal("load in-cluster kubernetes config", err)
	}
	return cfg
}

func fatal(msg string, err error) {
	slog.Error("mcp2rest fatal", "message", msg, "error", err)
	os.Exit(1)
}
