// Package slog writes all three OpenTelemetry signals to a log/slog logger for
// local development, correlated by trace_id and span_id. Production builds
// replace these exporters with OTLP exporters without changing business code.
//
//   - [SpanExporter] implements sdktrace.SpanExporter; install it with WithSyncer or WithBatcher.
//   - [MetricExporter] implements sdkmetric.Exporter; install it with a PeriodicReader.
//   - [LogExporter] implements sdklog.Exporter; install it with a LoggerProvider processor.
//
// Callers that also import the standard library log/slog must alias one of
// the two packages:
//
//	tp := sdktrace.NewTracerProvider(
//	    sdktrace.WithSyncer(slog.NewSpanExporter(stdslog.Default())),
//	)
//	otel.SetTracerProvider(tp)
//	defer tp.Shutdown(context.Background())
package slog
