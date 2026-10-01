package pipeline

import (
	"context"
	"testing"

	"github.com/karlrissland/homelab-api2mcp/internal/manifest"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
)

func TestMetricsStageRecordsSuccessAndLatency(t *testing.T) {
	t.Parallel()

	registry := prometheus.NewRegistry()
	metrics, err := NewMetrics(registry)
	if err != nil {
		t.Fatalf("NewMetrics() error = %v", err)
	}

	call := &CallContext{
		App:      testApp(),
		ToolName: "list_repos",
	}
	err = NewExecutor(
		NewMetricsStage(metrics),
		NewStage("noop", func(context.Context, *CallContext) error { return nil }),
	).Run(context.Background(), call)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got := testCounterValue(t, metrics.requests, "demo-app", "list_repos", "success"); got != 1 {
		t.Fatalf("request counter = %v, want 1", got)
	}
	if got := testHistogramCount(t, registry, metricsNamespace+"_"+toolRequestLatencySeconds, "demo-app", "list_repos", "success"); got != 1 {
		t.Fatalf("latency histogram count = %d, want 1", got)
	}
}

func TestMetricsStageRecordsStageFailureOutcome(t *testing.T) {
	t.Parallel()

	registry := prometheus.NewRegistry()
	metrics, err := NewMetrics(registry)
	if err != nil {
		t.Fatalf("NewMetrics() error = %v", err)
	}

	call := &CallContext{
		App:      testApp(),
		ToolName: "list_repos",
	}
	err = NewExecutor(
		NewMetricsStage(metrics),
		NewStage(authzStageName, func(context.Context, *CallContext) error { return ErrTierForbidden }),
	).Run(context.Background(), call)
	if err == nil {
		t.Fatal("Run() error = nil, want non-nil")
	}

	if got := testCounterValue(t, metrics.requests, "demo-app", "list_repos", authzStageName); got != 1 {
		t.Fatalf("request counter = %v, want 1", got)
	}
	if got := testHistogramCount(t, registry, metricsNamespace+"_"+toolRequestLatencySeconds, "demo-app", "list_repos", authzStageName); got != 1 {
		t.Fatalf("latency histogram count = %d, want 1", got)
	}
}

func testApp() manifest.App {
	return manifest.App{Name: "demo-app"}
}

func testCounterValue(t *testing.T, counter *prometheus.CounterVec, app, tool, outcome string) float64 {
	t.Helper()
	return testutil.ToFloat64(counter.WithLabelValues(app, tool, outcome))
}

func testHistogramCount(t *testing.T, registry *prometheus.Registry, metricName, app, tool, outcome string) uint64 {
	t.Helper()

	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("Gather() error = %v", err)
	}
	for _, family := range families {
		if family.GetName() != metricName {
			continue
		}
		for _, metric := range family.GetMetric() {
			if labelsMatch(metric, map[string]string{
				"app":     app,
				"tool":    tool,
				"outcome": outcome,
			}) {
				return metric.GetHistogram().GetSampleCount()
			}
		}
	}
	t.Fatalf("histogram %q with labels %s/%s/%s not found", metricName, app, tool, outcome)
	return 0
}

func labelsMatch(metric *dto.Metric, want map[string]string) bool {
	got := make(map[string]string, len(metric.GetLabel()))
	for _, label := range metric.GetLabel() {
		got[label.GetName()] = label.GetValue()
	}
	for name, value := range want {
		if got[name] != value {
			return false
		}
	}
	return true
}
