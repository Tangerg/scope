package slog

import (
	"context"
	"errors"
	"fmt"
	stdslog "log/slog"
	"sync/atomic"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

var errNilResourceMetrics = errors.New("otel/slog: resource metrics must not be nil")

// MetricExporter writes one slog record per metric, including its instrument,
// unit, scope, and data points. It preserves SDK accumulation defaults and
// provides local diagnostics without additional delivery guarantees.
type MetricExporter struct {
	logger   *stdslog.Logger
	shutdown atomic.Bool
}

func NewMetricExporter(logger *stdslog.Logger) *MetricExporter {
	if logger == nil {
		logger = stdslog.Default()
	}
	return &MetricExporter{logger: logger}
}

func (m *MetricExporter) Temporality(k sdkmetric.InstrumentKind) metricdata.Temporality {
	return sdkmetric.DefaultTemporalitySelector(k)
}

func (m *MetricExporter) Aggregation(k sdkmetric.InstrumentKind) sdkmetric.Aggregation {
	return sdkmetric.DefaultAggregationSelector(k)
}

// Export reports cancellation, nil input, and shutdown. log/slog does not
// expose handler failures, so they cannot become collection errors.
func (m *MetricExporter) Export(ctx context.Context, rm *metricdata.ResourceMetrics) error {
	if m.shutdown.Load() {
		return sdkmetric.ErrExporterShutdown
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if rm == nil {
		return errNilResourceMetrics
	}
	for _, sm := range rm.ScopeMetrics {
		for _, metric := range sm.Metrics {
			if err := ctx.Err(); err != nil {
				return err
			}
			if m.shutdown.Load() {
				return sdkmetric.ErrExporterShutdown
			}
			attrs := []stdslog.Attr{
				stdslog.String("metric", metric.Name),
				stdslog.String("scope", sm.Scope.Name),
			}
			if metric.Unit != "" {
				attrs = append(attrs, stdslog.String("unit", metric.Unit))
			}
			attrs = append(attrs, stdslog.String("value", m.summarize(metric.Data)))
			m.logger.LogAttrs(ctx, stdslog.LevelInfo, "metric", attrs...)
		}
	}
	return nil
}

func (m *MetricExporter) ForceFlush(ctx context.Context) error { return ctx.Err() }

// Shutdown returns [sdkmetric.ErrExporterShutdown] when the exporter is
// already shut down, as the sdkmetric.Exporter contract requires.
func (m *MetricExporter) Shutdown(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !m.shutdown.CompareAndSwap(false, true) {
		return sdkmetric.ErrExporterShutdown
	}
	return nil
}

func (m *MetricExporter) summarize(data metricdata.Aggregation) string {
	switch d := data.(type) {
	case metricdata.Sum[int64]:
		return m.summarizeSum(d.DataPoints)
	case metricdata.Sum[float64]:
		return m.summarizeSum(d.DataPoints)
	case metricdata.Gauge[int64]:
		return m.summarizeGauge(d.DataPoints)
	case metricdata.Gauge[float64]:
		return m.summarizeGauge(d.DataPoints)
	case metricdata.Histogram[int64]:
		return m.summarizeHistogram(d.DataPoints)
	case metricdata.Histogram[float64]:
		return m.summarizeHistogram(d.DataPoints)
	default:
		return fmt.Sprintf("%T", data)
	}
}

func (m *MetricExporter) summarizeSum[N int64 | float64](points []metricdata.DataPoint[N]) string {
	var total N
	for _, point := range points {
		total += point.Value
	}
	return fmt.Sprintf("sum=%v points=%d", total, len(points))
}

func (m *MetricExporter) summarizeGauge[N int64 | float64](points []metricdata.DataPoint[N]) string {
	if len(points) == 0 {
		return "gauge=<none>"
	}
	return fmt.Sprintf("gauge=%v points=%d", points[len(points)-1].Value, len(points))
}

func (m *MetricExporter) summarizeHistogram[N int64 | float64](points []metricdata.HistogramDataPoint[N]) string {
	var count uint64
	var sum N
	for _, point := range points {
		count += point.Count
		sum += point.Sum
	}
	return fmt.Sprintf("hist count=%d sum=%v points=%d", count, sum, len(points))
}
