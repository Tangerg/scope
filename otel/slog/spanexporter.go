package slog

import (
	"context"
	stdslog "log/slog"
	"sync/atomic"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// SpanExporter writes one slog record per finished span. Error spans use the
// error log level. Attributes retain their OTel keys in the attributes group.
// Trace structure is represented by trace_id, span_id, and parent_span_id.
type SpanExporter struct {
	logger   *stdslog.Logger
	shutdown atomic.Bool
}

func NewSpanExporter(logger *stdslog.Logger) *SpanExporter {
	if logger == nil {
		logger = stdslog.Default()
	}
	return &SpanExporter{logger: logger}
}

// ExportSpans returns only context errors, because log/slog does not expose
// handler failures. After Shutdown it performs no work.
func (s *SpanExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	if s.shutdown.Load() {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, span := range spans {
		if err := ctx.Err(); err != nil {
			return err
		}
		if s.shutdown.Load() {
			return nil
		}
		attrs := make([]stdslog.Attr, 0, 7)

		sc := span.SpanContext()
		attrs = append(attrs,
			stdslog.String("trace_id", sc.TraceID().String()),
			stdslog.String("span_id", sc.SpanID().String()),
			stdslog.String("name", span.Name()),
			stdslog.Duration("duration", span.EndTime().Sub(span.StartTime())),
		)

		if parent := span.Parent(); parent.HasSpanID() {
			attrs = append(attrs, stdslog.String("parent_span_id", parent.SpanID().String()))
		}

		attributes := make([]stdslog.Attr, 0, len(span.Attributes()))
		for _, kv := range span.Attributes() {
			attributes = append(attributes, stdslog.Any(string(kv.Key), kv.Value.AsInterface()))
		}
		attrs = append(attrs, stdslog.GroupAttrs("attributes", attributes...))

		if evs := span.Events(); len(evs) > 0 {
			names := make([]string, len(evs))
			for i, ev := range evs {
				names[i] = ev.Name
			}
			attrs = append(attrs, stdslog.Any("events", names))
		}

		level := stdslog.LevelInfo
		msg := "span"
		if status := span.Status(); status.Code == codes.Error {
			level = stdslog.LevelError
			if status.Description != "" {
				msg = "span (error): " + status.Description
			} else {
				msg = "span (error)"
			}
		}

		s.logger.LogAttrs(ctx, level, msg, attrs...)
	}
	return nil
}

func (s *SpanExporter) Shutdown(ctx context.Context) error {
	if s.shutdown.Load() {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.shutdown.Store(true)
	return nil
}
