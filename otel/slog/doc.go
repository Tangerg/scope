// Package slog writes all three OpenTelemetry signals to a log/slog logger for
// local development, correlated by trace_id and span_id. Production builds
// replace these exporters with OTLP exporters without changing business code.
//
// Span and log attributes retain their OTel keys inside an attributes group,
// keeping them distinct from the SDK metadata and slog record fields. JSON
// consumers read an application attribute at attributes[key]; TextHandler
// renders it as attributes.key.
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
