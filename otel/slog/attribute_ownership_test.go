package slog_test

import (
	"bytes"
	"context"
	"encoding/json/v2"
	stdslog "log/slog"
	"slices"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/Tangerg/scope/otel/slog"
)

func TestSpanExporterSeparatesAttributesFromSpanMetadata(t *testing.T) {
	spanContext := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2},
	})
	parent := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: spanContext.TraceID(), SpanID: trace.SpanID{3},
	})
	keys := []string{"trace_id", "span_id", "parent_span_id", "name", "duration", "events", "time", "level", "msg", "attributes", "gen_ai.provider.name"}
	attributes := make([]attribute.KeyValue, 0, len(keys))
	for _, key := range keys {
		attributes = append(attributes, attribute.String(key, "application."+key))
	}
	span := tracetest.SpanStub{
		Name: "native.span", SpanContext: spanContext, Parent: parent,
		StartTime: time.Unix(0, 0), EndTime: time.Unix(1, 0),
		Attributes: attributes, Events: []sdktrace.Event{{Name: "native.event"}},
	}.Snapshot()
	var output bytes.Buffer
	exporter := slog.NewSpanExporter(stdslog.New(stdslog.NewJSONHandler(&output, nil)))
	if err := exporter.ExportSpans(t.Context(), []sdktrace.ReadOnlySpan{span}); err != nil {
		t.Fatal(err)
	}
	var record struct {
		TraceID      string            `json:"trace_id"`
		SpanID       string            `json:"span_id"`
		ParentSpanID string            `json:"parent_span_id"`
		Name         string            `json:"name"`
		Duration     int64             `json:"duration"`
		Events       []string          `json:"events"`
		Time         time.Time         `json:"time"`
		Level        string            `json:"level"`
		Message      string            `json:"msg"`
		Attributes   map[string]string `json:"attributes"`
	}
	if err := json.Unmarshal(output.Bytes(), &record, json.RejectUnknownMembers(true)); err != nil {
		t.Fatalf("decode exported span: %v; output = %s", err, output.Bytes())
	}
	if record.TraceID != spanContext.TraceID().String() || record.SpanID != spanContext.SpanID().String() || record.ParentSpanID != parent.SpanID().String() {
		t.Fatalf("span context changed: %+v", record)
	}
	if record.Name != "native.span" || record.Duration != int64(time.Second) || !slices.Equal(record.Events, []string{"native.event"}) {
		t.Fatalf("span metadata changed: %+v", record)
	}
	if record.Time.IsZero() || record.Level != "INFO" || record.Message != "span" {
		t.Fatalf("slog record metadata changed: %+v", record)
	}
	if len(record.Attributes) != len(keys) {
		t.Fatalf("attributes = %v, want %d attributes", record.Attributes, len(keys))
	}
	for _, key := range keys {
		if got, want := record.Attributes[key], "application."+key; got != want {
			t.Errorf("attributes[%q] = %q, want %q", key, got, want)
		}
	}
}

func TestLogExporterSeparatesAttributesFromRecordMetadata(t *testing.T) {
	for _, populated := range []bool{true, false} {
		name := "without native context"
		if populated {
			name = "with native context"
		}
		t.Run(name, func(t *testing.T) {
			spanContext := trace.NewSpanContext(trace.SpanContextConfig{
				TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2},
			})
			ctx := t.Context()
			scopeName, eventName := "", ""
			if populated {
				ctx = trace.ContextWithSpanContext(ctx, spanContext)
				scopeName, eventName = "native.scope", "native.event"
			}
			var output bytes.Buffer
			exporter := slog.NewLogExporter(stdslog.New(stdslog.NewJSONHandler(&output, nil)))
			provider := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exporter)))
			t.Cleanup(func() {
				if err := provider.Shutdown(context.Background()); err != nil {
					t.Error(err)
				}
			})
			var source otellog.Record
			source.SetEventName(eventName)
			source.SetSeverity(otellog.SeverityInfo)
			source.SetBody(attribute.StringValue("native.body"))
			keys := []string{"trace_id", "span_id", "scope", "event_name", "time", "level", "msg", "attributes", "gen_ai.conversation.id"}
			for _, key := range keys {
				source.AddAttributes(attribute.String(key, "application."+key))
			}
			provider.Logger(scopeName).Emit(ctx, source)
			var record struct {
				TraceID    string            `json:"trace_id"`
				SpanID     string            `json:"span_id"`
				Scope      string            `json:"scope"`
				EventName  string            `json:"event_name"`
				Time       time.Time         `json:"time"`
				Level      string            `json:"level"`
				Message    string            `json:"msg"`
				Attributes map[string]string `json:"attributes"`
			}
			if err := json.Unmarshal(output.Bytes(), &record, json.RejectUnknownMembers(true)); err != nil {
				t.Fatalf("decode exported log: %v; output = %s", err, output.Bytes())
			}
			if populated {
				if record.TraceID != spanContext.TraceID().String() || record.SpanID != spanContext.SpanID().String() {
					t.Fatalf("trace context changed: %+v", record)
				}
			} else if record.TraceID != "" || record.SpanID != "" {
				t.Fatalf("attributes created a trace context: %+v", record)
			}
			if record.Scope != scopeName || record.EventName != eventName {
				t.Fatalf("record metadata changed: %+v", record)
			}
			if record.Time.IsZero() || record.Level != "INFO" || record.Message != "native.body" {
				t.Fatalf("slog record metadata changed: %+v", record)
			}
			if len(record.Attributes) != len(keys) {
				t.Fatalf("attributes = %v, want %d attributes", record.Attributes, len(keys))
			}
			for _, key := range keys {
				if got, want := record.Attributes[key], "application."+key; got != want {
					t.Errorf("attributes[%q] = %q, want %q", key, got, want)
				}
			}
		})
	}
}
