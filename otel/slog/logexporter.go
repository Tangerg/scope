package slog

import (
	"context"
	stdslog "log/slog"
	"sync/atomic"

	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

// LogExporter writes one slog record per OTel log record. Severity maps to
// slog levels; trace_id and span_id come from the record's own trace context.
// Use a logger that does not feed this LoggerProvider to avoid a feedback loop.
type LogExporter struct {
	logger   *stdslog.Logger
	shutdown atomic.Bool
}

func NewLogExporter(logger *stdslog.Logger) *LogExporter {
	if logger == nil {
		logger = stdslog.Default()
	}
	return &LogExporter{logger: logger}
}

// Export returns only context errors and [sdklog.ErrExporterShutdown] after
// Shutdown.
func (l *LogExporter) Export(ctx context.Context, records []sdklog.Record) error {
	if l.shutdown.Load() {
		return sdklog.ErrExporterShutdown
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, rec := range records {
		if err := ctx.Err(); err != nil {
			return err
		}
		if l.shutdown.Load() {
			return sdklog.ErrExporterShutdown
		}
		attrs := make([]stdslog.Attr, 0, rec.AttributesLen()+3)
		if tid := rec.TraceID(); tid.IsValid() {
			attrs = append(attrs, stdslog.String("trace_id", tid.String()))
		}
		if sid := rec.SpanID(); sid.IsValid() {
			attrs = append(attrs, stdslog.String("span_id", sid.String()))
		}
		if scope := rec.InstrumentationScope().Name; scope != "" {
			attrs = append(attrs, stdslog.String("scope", scope))
		}
		if eventName := rec.EventName(); eventName != "" {
			attrs = append(attrs, stdslog.String("event_name", eventName))
		}
		rec.WalkAttributes(func(kv attribute.KeyValue) bool {
			attrs = append(attrs, logKVToSlog(kv))
			return true
		})
		l.logger.LogAttrs(ctx, severityToLevel(rec.Severity()), rec.Body().String(), attrs...)
	}
	return nil
}

func (l *LogExporter) ForceFlush(ctx context.Context) error {
	if l.shutdown.Load() {
		return nil
	}
	return ctx.Err()
}

func (l *LogExporter) Shutdown(ctx context.Context) error {
	if l.shutdown.Load() {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	l.shutdown.Store(true)
	return nil
}

func severityToLevel(s otellog.Severity) stdslog.Level {
	switch {
	case s >= otellog.SeverityError:
		return stdslog.LevelError
	case s >= otellog.SeverityWarn:
		return stdslog.LevelWarn
	case s >= otellog.SeverityInfo:
		return stdslog.LevelInfo
	default:
		return stdslog.LevelDebug
	}
}

func logKVToSlog(kv attribute.KeyValue) stdslog.Attr {
	key := string(kv.Key)
	switch v := kv.Value; v.Type() {
	case attribute.BOOL:
		return stdslog.Bool(key, v.AsBool())
	case attribute.INT64:
		return stdslog.Int64(key, v.AsInt64())
	case attribute.FLOAT64:
		return stdslog.Float64(key, v.AsFloat64())
	case attribute.STRING:
		return stdslog.String(key, v.AsString())
	default:
		return stdslog.String(key, v.String())
	}
}
