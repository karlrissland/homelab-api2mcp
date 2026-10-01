package mcpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"

	"github.com/karlrissland/homelab-api2mcp/internal/adminapi"
	"github.com/karlrissland/homelab-api2mcp/internal/discovery"
	"github.com/karlrissland/homelab-api2mcp/internal/keys"
	"github.com/karlrissland/homelab-api2mcp/internal/render"
	"github.com/karlrissland/homelab-api2mcp/internal/skillstools"
)

func TestRuntimeHandlerServesMetricsEndpoint(t *testing.T) {
	t.Parallel()

	client := kubernetesfake.NewSimpleClientset()
	table, err := discovery.New(client)
	if err != nil {
		t.Fatalf("discovery.New() error = %v", err)
	}

	store := keys.NewStore()
	writer, err := keys.NewSecretWriter(client)
	if err != nil {
		t.Fatalf("keys.NewSecretWriter() error = %v", err)
	}
	admin, err := adminapi.New(client, table, store, writer)
	if err != nil {
		t.Fatalf("adminapi.New() error = %v", err)
	}

	skillGVR := schema.GroupVersionResource{Group: skillstools.Group, Version: skillstools.Version, Resource: skillstools.Resource}
	skillClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		skillGVR: skillstools.Kind + "List",
	})

	metrics, logger, metricsHandler := testRuntimeObservability(t)
	metrics.Observe("demo", "list_repos", "success", 50*time.Millisecond)
	handler, err := NewRuntimeHandler(table, store, render.New(http.DefaultClient), nil, admin, skillClient, metrics, logger, metricsHandler)
	if err != nil {
		t.Fatalf("NewRuntimeHandler() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, metricsPath, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "mcp2rest_tool_requests_total") {
		t.Fatalf("GET /metrics body missing expected metric name: %s", body)
	}
}
