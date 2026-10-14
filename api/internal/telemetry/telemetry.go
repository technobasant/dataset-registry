// Package telemetry sets up structured logs, traces and the Prometheus registry.
package telemetry

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// Logger returns a JSON logger at the level named by LOG_LEVEL.
func Logger() *slog.Logger {
	var level slog.Level
	_ = level.UnmarshalText([]byte(strings.ToUpper(os.Getenv("LOG_LEVEL"))))
	return slog.New(traceHandler{slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})})
}

// traceHandler adds trace and span IDs to records logged inside a span.
type traceHandler struct{ slog.Handler }

func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return h.Handler.Handle(ctx, r)
}

func (h traceHandler) WithAttrs(a []slog.Attr) slog.Handler {
	return traceHandler{h.Handler.WithAttrs(a)}
}
func (h traceHandler) WithGroup(n string) slog.Handler { return traceHandler{h.Handler.WithGroup(n)} }

// Tracing installs a tracer provider. Spans are exported over OTLP/HTTP when
// OTEL_EXPORTER_OTLP_ENDPOINT is set; otherwise they are created (so logs carry
// trace IDs) and dropped.
func Tracing(ctx context.Context, service string) (func(context.Context) error, error) {
	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(semconv.SchemaURL,
		semconv.ServiceName(service)))
	if err != nil {
		return nil, err
	}
	opts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res)}
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" {
		exp, err := otlptracehttp.New(ctx)
		if err != nil {
			return nil, err
		}
		opts = append(opts, sdktrace.WithBatcher(exp))
	}
	tp := sdktrace.NewTracerProvider(opts...)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	return tp.Shutdown, nil
}

// Tracer is the registry's tracer.
func Tracer() trace.Tracer { return otel.Tracer("dataset-registry") }

// Metrics are the registry's Prometheus collectors.
type Metrics struct {
	Registry *prometheus.Registry

	HTTPDuration *prometheus.HistogramVec

	LineageEvents   *prometheus.CounterVec
	LineageDuration prometheus.Histogram

	Staleness         *prometheus.GaugeVec
	SLOSeconds        *prometheus.GaugeVec
	SLOStatus         *prometheus.GaugeVec
	DatasetsByStatus  *prometheus.GaugeVec
	EvaluationSeconds prometheus.Histogram
	DetectionDelay    prometheus.Histogram
	Transitions       *prometheus.CounterVec

	ChangeRequests *prometheus.CounterVec
}

// NewMetrics registers every collector on a fresh registry.
func NewMetrics() *Metrics {
	r := prometheus.NewRegistry()
	r.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	f := func(c prometheus.Collector) { r.MustRegister(c) }
	dataset := []string{"dataset", "owner_team", "tier"}
	m := &Metrics{
		Registry: r,
		HTTPDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "registry_http_request_duration_seconds", Help: "HTTP request latency by route.",
			Buckets: []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5},
		}, []string{"method", "route", "code"}),
		LineageEvents: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "registry_lineage_events_total", Help: "OpenLineage events received, by type and outcome.",
		}, []string{"event_type", "result"}),
		LineageDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "registry_lineage_ingest_seconds", Help: "Time to validate and store one OpenLineage event.",
			Buckets: []float64{.0005, .001, .0025, .005, .01, .025, .05, .1, .25},
		}),
		Staleness: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "registry_dataset_staleness_seconds", Help: "Seconds since the last successful write, at the last evaluation.",
		}, dataset),
		SLOSeconds: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "registry_dataset_freshness_slo_seconds", Help: "Maximum staleness the owner committed to.",
		}, dataset),
		SLOStatus: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "registry_dataset_slo_status", Help: "1 for the dataset's current freshness status, 0 otherwise.",
		}, append(dataset, "status")),
		DatasetsByStatus: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "registry_datasets_by_slo_status", Help: "Datasets with a freshness SLO, by status.",
		}, []string{"status"}),
		EvaluationSeconds: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "registry_freshness_evaluation_seconds", Help: "Time to evaluate every SLO once.",
			Buckets: prometheus.ExponentialBuckets(0.001, 2, 14),
		}),
		DetectionDelay: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "registry_freshness_breach_detection_delay_seconds", Help: "Time from a dataset missing its SLO to the evaluator noticing.",
			Buckets: []float64{.1, .25, .5, 1, 2, 5, 10, 15, 30, 60, 120},
		}),
		Transitions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "registry_slo_transitions_total", Help: "Freshness status changes.",
		}, []string{"to"}),
		ChangeRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "registry_change_request_events_total", Help: "Change request lifecycle events.",
		}, []string{"event"}),
	}
	for _, c := range []prometheus.Collector{m.HTTPDuration, m.LineageEvents, m.LineageDuration, m.Staleness,
		m.SLOSeconds, m.SLOStatus, m.DatasetsByStatus, m.EvaluationSeconds, m.DetectionDelay, m.Transitions, m.ChangeRequests} {
		f(c)
	}
	return m
}
