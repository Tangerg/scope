package slog_test

import (
	"context"
	"errors"
	stdslog "log/slog"
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
}
