package pipeline

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	metricsNamespace          = "mcp2rest"
	toolRequestsMetricName    = "tool_requests_total"
	toolRequestLatencySeconds = "tool_request_duration_seconds"
)

// Metrics records Prometheus observations for completed proxied tool calls.
type Metrics struct {
	requests *prometheus.CounterVec
	latency  *prometheus.HistogramVec
}

// NewMetrics registers the Prometheus collectors used by the metrics stage.
func NewMetrics(registerer prometheus.Registerer) (*Metrics, error) {
	if registerer == nil {
		return nil, fmt.Errorf("new metrics: registerer is required")
	}

	requests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Name:      toolRequestsMetricName,
		Help:      "Total number of completed mcp2rest proxied tool calls by app, tool, and outcome.",
	}, []string{"app", "tool", "outcome"})
	latency := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: metricsNamespace,
		Name:      toolRequestLatencySeconds,
		Help:      "Latency of completed mcp2rest proxied tool calls by app, tool, and outcome.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"app", "tool", "outcome"})

	registeredRequests, err := registerCounterVec(registerer, toolRequestsMetricName, requests)
	if err != nil {
		return nil, err
	}
	registeredLatency, err := registerHistogramVec(registerer, toolRequestLatencySeconds, latency)
	if err != nil {
		return nil, err
	}

	return &Metrics{
		requests: registeredRequests,
		latency:  registeredLatency,
	}, nil
}

// NewMetricsStage builds the metrics stage.
func NewMetricsStage(metrics *Metrics) Stage {
	return NewStage(metricsStageName, func(_ context.Context, call *CallContext) error {
		if metrics == nil {
			return nil
		}

		startedAt := time.Now()
		call.addFinalizer(func(err error) {
			metrics.Observe(callAppName(call), callToolName(call), outcomeLabel(err), time.Since(startedAt))
		})
		return nil
	})
}

// Observe records one completed proxied tool call.
func (m *Metrics) Observe(app, tool, outcome string, duration time.Duration) {
	if m == nil {
		return
	}
	m.requests.WithLabelValues(app, tool, outcome).Inc()
	m.latency.WithLabelValues(app, tool, outcome).Observe(duration.Seconds())
}

func outcomeLabel(err error) string {
	if err == nil {
		return "success"
	}

	var stageErr *StageError
	if errors.As(err, &stageErr) && stageErr.Stage != "" {
		return stageErr.Stage
	}

	return "executor"
}

func callAppName(call *CallContext) string {
	if call == nil || call.App.Name == "" {
		return "unknown"
	}
	return call.App.Name
}

func callToolName(call *CallContext) string {
	switch {
	case call == nil:
		return "unknown"
	case call.ToolName != "":
		return call.ToolName
	case call.Tool.Name != "":
		return call.Tool.Name
	default:
		return "unknown"
	}
}

func registerCounterVec(registerer prometheus.Registerer, name string, collector *prometheus.CounterVec) (*prometheus.CounterVec, error) {
	if err := registerer.Register(collector); err != nil {
		var alreadyRegistered prometheus.AlreadyRegisteredError
		if errors.As(err, &alreadyRegistered) {
			existing, ok := alreadyRegistered.ExistingCollector.(*prometheus.CounterVec)
			if !ok {
				return nil, fmt.Errorf("register counter %q: existing collector type %T", name, alreadyRegistered.ExistingCollector)
			}
			return existing, nil
		}
		return nil, fmt.Errorf("register counter %q: %w", name, err)
	}
	return collector, nil
}

func registerHistogramVec(registerer prometheus.Registerer, name string, collector *prometheus.HistogramVec) (*prometheus.HistogramVec, error) {
	if err := registerer.Register(collector); err != nil {
		var alreadyRegistered prometheus.AlreadyRegisteredError
		if errors.As(err, &alreadyRegistered) {
			existing, ok := alreadyRegistered.ExistingCollector.(*prometheus.HistogramVec)
			if !ok {
				return nil, fmt.Errorf("register histogram %q: existing collector type %T", name, alreadyRegistered.ExistingCollector)
			}
			return existing, nil
		}
		return nil, fmt.Errorf("register histogram %q: %w", name, err)
	}
	return collector, nil
}
