package slog_test

import (
	"context"
	"errors"
	stdslog "log/slog"
	"reflect"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/Tangerg/scope/otel/slog"
)

func TestMetricExporter_WritesOneRecordPerMetric(t *testing.T) {
	cap := &captureHandler{}
	logger := stdslog.New(cap)

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	meter := mp.Meter("test")

	ctr, err := meter.Int64Counter("runs.started")
	if err != nil {
		t.Fatalf("counter: %v", err)
	}
	ctr.Add(t.Context(), 3, metric.WithAttributes(attribute.String("k", "v")))

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}

	exp := slog.NewMetricExporter(logger)
	if err := exp.Export(t.Context(), &rm); err != nil {
		t.Fatalf("export: %v", err)
	}

	found := false
	for _, r := range cap.Records() {
		if r.Message != "metric" {
			continue
		}
		r.Attrs(func(a stdslog.Attr) bool {
			if a.Key == "metric" && a.Value.String() == "runs.started" {
				found = true
			}
			return true
		})
	}
	if !found {
		t.Fatal("expected a metric record for runs.started")
	}
}

func TestMetricExporterLifecycle(t *testing.T) {
	exporter := slog.NewMetricExporter(stdslog.New(&captureHandler{}))
	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	if err := exporter.Export(canceled, &metricdata.ResourceMetrics{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Export canceled error = %v", err)
	}
	if err := exporter.Export(t.Context(), nil); err == nil {
		t.Fatal("Export accepted nil ResourceMetrics")
	}
	if err := exporter.ForceFlush(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("ForceFlush canceled error = %v", err)
	}
	if err := exporter.Shutdown(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Shutdown canceled error = %v", err)
	}
	if err := exporter.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := exporter.Export(t.Context(), &metricdata.ResourceMetrics{}); !errors.Is(err, sdkmetric.ErrExporterShutdown) {
		t.Fatalf("Export after shutdown = %v, want ErrExporterShutdown", err)
	}
	if err := exporter.ForceFlush(t.Context()); err != nil {
		t.Fatalf("ForceFlush after shutdown = %v, want nil", err)
	}
	if err := exporter.ForceFlush(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("ForceFlush canceled after shutdown = %v, want context.Canceled", err)
	}
	if err := exporter.Shutdown(t.Context()); !errors.Is(err, sdkmetric.ErrExporterShutdown) {
		t.Fatalf("Shutdown after shutdown = %v, want ErrExporterShutdown", err)
	}
}

func TestMetricExporterPreservesExponentialHistogramMeasurements(t *testing.T) {
	handler := &captureHandler{}
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(reader),
		sdkmetric.WithView(sdkmetric.NewView(
			sdkmetric.Instrument{Name: "request.latency"},
			sdkmetric.Stream{Aggregation: sdkmetric.AggregationBase2ExponentialHistogram{MaxSize: 160, MaxScale: 20}},
		)),
	)
	t.Cleanup(func() {
		if err := provider.Shutdown(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	})
	histogram, err := provider.Meter("test").Float64Histogram("request.latency", metric.WithUnit("s"))
	if err != nil {
		t.Fatal(err)
	}
	histogram.Record(t.Context(), 2)
	histogram.Record(t.Context(), 3)
	var collected metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &collected); err != nil {
		t.Fatal(err)
	}
	if err := slog.NewMetricExporter(stdslog.New(handler)).Export(t.Context(), &collected); err != nil {
		t.Fatal(err)
	}
	records := handler.Records()
	if len(records) != 1 {
		t.Fatalf("exported %d records, want one metric", len(records))
	}
	want := map[string]any{
		"metric": "request.latency", "scope": "test", "unit": "s",
		"value": "exp_hist count=2 sum=5 points=1",
	}
	if got := attrMap(records[0]); !reflect.DeepEqual(got, want) {
		t.Fatalf("exported attributes = %v, want %v", got, want)
	}
}

func TestMetricExporterRejectsMissingAggregation(t *testing.T) {
	handler := &captureHandler{}
	metrics := metricdata.ResourceMetrics{ScopeMetrics: []metricdata.ScopeMetrics{{
		Metrics: []metricdata.Metrics{{Name: "missing"}},
	}}}
	if err := slog.NewMetricExporter(stdslog.New(handler)).Export(t.Context(), &metrics); err == nil {
		t.Fatal("missing aggregation was acknowledged")
	}
	if len(handler.Records()) != 0 {
		t.Fatal("invalid aggregation was logged as a metric")
	}
}
