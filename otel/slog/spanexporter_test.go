package slog_test

import (
	"context"
	"errors"
	stdslog "log/slog"
	"sync"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/Tangerg/scope/otel/slog"
)

type captureHandler struct {
	mu      sync.Mutex
	records []stdslog.Record
}

func (c *captureHandler) Enabled(context.Context, stdslog.Level) bool { return true }
func (c *captureHandler) Handle(_ context.Context, r stdslog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, r.Clone())
	return nil
}
func (c *captureHandler) WithAttrs([]stdslog.Attr) stdslog.Handler { return c }
func (c *captureHandler) WithGroup(string) stdslog.Handler         { return c }

func (c *captureHandler) Records() []stdslog.Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]stdslog.Record, len(c.records))
	copy(out, c.records)
	return out
}

func attrMap(r stdslog.Record) map[string]any {
	m := make(map[string]any, r.NumAttrs())
	r.Attrs(func(a stdslog.Attr) bool {
		if a.Value.Kind() == stdslog.KindGroup {
			group := make(map[string]any)
			for _, attr := range a.Value.Group() {
				group[attr.Key] = attr.Value.Any()
			}
			m[a.Key] = group
		} else {
			m[a.Key] = a.Value.Any()
		}
		return true
	})
	return m
}

func newTestProvider(exporter sdktrace.SpanExporter) *sdktrace.TracerProvider {
	return sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSyncer(exporter))
}

func TestExporter_SuccessSpan(t *testing.T) {
	handler := &captureHandler{}
	logger := stdslog.New(handler)
	exp := slog.NewSpanExporter(logger)

	tp := newTestProvider(exp)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	tracer := tp.Tracer("test")
	_, span := tracer.Start(t.Context(), "unit.test.op")
	span.SetAttributes(
		attribute.String("gen_ai.provider.name", "openai"),
		attribute.Int("gen_ai.request.max_tokens", 1024),
	)
	span.AddEvent("checkpoint")
	span.End()

	records := handler.Records()
	if len(records) != 1 {
		t.Fatalf("want 1 record, got %d", len(records))
	}

	r := records[0]
	if r.Level != stdslog.LevelInfo {
		t.Errorf("want Info level, got %v", r.Level)
	}
	if r.Message != "span" {
		t.Errorf("want message %q, got %q", "span", r.Message)
	}

	attrs := attrMap(r)
	if attrs["name"] != "unit.test.op" {
		t.Errorf("want name=unit.test.op, got %v", attrs["name"])
	}
	attributes, ok := attrs["attributes"].(map[string]any)
	if !ok {
		t.Fatalf("attributes group missing: %v", attrs)
	}
	if attributes["gen_ai.provider.name"] != "openai" {
		t.Errorf("want gen_ai.provider.name=openai, got %v", attributes["gen_ai.provider.name"])
	}
	if attributes["gen_ai.request.max_tokens"] != int64(1024) {
		t.Errorf("want gen_ai.request.max_tokens=1024, got %v", attributes["gen_ai.request.max_tokens"])
	}
	if _, ok := attrs["trace_id"]; !ok {
		t.Error("trace_id attribute missing")
	}
	if _, ok := attrs["span_id"]; !ok {
		t.Error("span_id attribute missing")
	}
	if _, ok := attrs["duration"]; !ok {
		t.Error("duration attribute missing")
	}
	if events, ok := attrs["events"].([]string); !ok || len(events) != 1 || events[0] != "checkpoint" {
		t.Errorf("events mismatch: %v", attrs["events"])
	}
}

func TestExporter_ErrorSpan(t *testing.T) {
	handler := &captureHandler{}
	exp := slog.NewSpanExporter(stdslog.New(handler))

	tp := newTestProvider(exp)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	_, span := tp.Tracer("test").Start(t.Context(), "failing.op")
	span.RecordError(errors.New("boom"))
	span.SetStatus(codes.Error, "boom")
	span.End()

	records := handler.Records()
	if len(records) != 1 {
		t.Fatalf("want 1 record, got %d", len(records))
	}
	r := records[0]
	if r.Level != stdslog.LevelError {
		t.Errorf("want Error level, got %v", r.Level)
	}
	if r.Message != "span (error): boom" {
		t.Errorf("unexpected message: %q", r.Message)
	}
}

func TestExporter_ChildSpan_RecordsParent(t *testing.T) {
	handler := &captureHandler{}
	exp := slog.NewSpanExporter(stdslog.New(handler))

	tp := newTestProvider(exp)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	tracer := tp.Tracer("test")
	ctx, parent := tracer.Start(t.Context(), "parent")
	_, child := tracer.Start(ctx, "child")
	child.End()
	parent.End()

	records := handler.Records()
	if len(records) != 2 {
		t.Fatalf("want 2 records, got %d", len(records))
	}

	// OTel exports finished spans in end-order, so child is first
	childAttrs := attrMap(records[0])
	parentAttrs := attrMap(records[1])

	parentSpanID, _ := parentAttrs["span_id"].(string)
	childParentID, _ := childAttrs["parent_span_id"].(string)
	if parentSpanID == "" || childParentID != parentSpanID {
		t.Errorf("child.parent_span_id=%q, want to equal parent.span_id=%q", childParentID, parentSpanID)
	}

	if _, has := parentAttrs["parent_span_id"]; has {
		t.Error("root span should not have parent_span_id")
	}
}

func TestExporter_NilLogger_UsesDefault(t *testing.T) {

	exp := slog.NewSpanExporter(nil)

	tp := newTestProvider(exp)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	_, span := tp.Tracer("test").Start(t.Context(), "smoke")
	span.End()
}

func TestExporter_Shutdown_ReturnsNil(t *testing.T) {
	exp := slog.NewSpanExporter(stdslog.Default())
	if err := exp.Shutdown(t.Context()); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
}

func TestSpanExporterLifecycle(t *testing.T) {
	capture := &captureHandler{}
	exporter := slog.NewSpanExporter(stdslog.New(capture))
	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	if err := exporter.ExportSpans(canceled, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("ExportSpans canceled error = %v", err)
	}
	if err := exporter.Shutdown(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Shutdown canceled error = %v", err)
	}
	if err := exporter.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	spans := []sdktrace.ReadOnlySpan{tracetest.SpanStub{Name: "must not be exported"}.Snapshot()}
	for _, ctx := range []context.Context{t.Context(), canceled} {
		if err := exporter.ExportSpans(ctx, spans); err != nil {
			t.Fatalf("ExportSpans after shutdown = %v, want nil", err)
		}
		if err := exporter.Shutdown(ctx); err != nil {
			t.Fatalf("Shutdown after shutdown = %v, want nil", err)
		}
	}
	if records := capture.Records(); len(records) != 0 {
		t.Fatalf("ExportSpans after shutdown wrote %d records", len(records))
	}
}
