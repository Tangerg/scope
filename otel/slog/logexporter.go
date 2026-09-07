package slog

import (
	"context"
	stdslog "log/slog"
	"sync/atomic"

	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

// LogExporter writes OpenTelemetry log records to a log/slog logger — the
// Logs leg of the dev observability triad, a sibling of [SpanExporter] and
// [MetricExporter] so all three OTel signals share one slog stream keyed by
// trace_id.
//
// Application code keeps calling slog (via the contrib otelslog bridge that
// feeds a LoggerProvider); installing this exporter on that provider is what
// makes logs as backend-swappable as traces/metrics — a production build
// swaps it for an OTLP log exporter (→ Datadog / Cloud Logging / ...) with
// zero business-code change. That swappability is the whole reason logs go
// through OTel rather than straight to slog.
//
// Install it on a LoggerProvider via a processor:
//
//	lp := sdklog.NewLoggerProvider(
//	    sdklog.WithProcessor(sdklog.NewSimpleProcessor(slog.NewLogExporter(logger))))
//
// Each OTel log record becomes one slog record: the record body is the
// message, the severity maps to the slog level, and trace_id / span_id come
// from the record's own trace context (the SDK fills them from the emitting
// span — native correlation, no manual stamping).
type LogExporter struct {
	logger   *stdslog.Logger
	shutdown atomic.Bool
}

// NewLogExporter writes records through [log/slog] so telemetry is readable in
// development without running a collector. The exporter adds no delivery
// guarantees of its own and is intended for local diagnostics.
func NewLogExporter(logger *stdslog.Logger) *LogExporter {
	if logger == nil {
		logger = stdslog.Default()
	}
	return &LogExporter{logger: logger}
}

// Export writes one slog record per OTel log record. It returns only context
// cancellation/deadline errors and [sdklog.ErrExporterShutdown] when closed.
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

// severityToLevel maps an OTel log severity (Trace..Fatal, 1-24) onto the
// nearest slog level: Error and above → Error, Warn band → Warn, Info band →
// Info, everything lower (Debug / Trace / Undefined) → Debug.
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

// logKVToSlog converts one OTel log key-value to a typed slog.Attr, keeping
// the common scalar kinds typed and falling back to a string rendering for
// composite kinds (bytes / slice / map).
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
