package history_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
	historyotel "github.com/Tangerg/scope/otel/history"
)

type historyRecorder struct {
	messages []chat.Message
	err      error
}

func (h *historyRecorder) Read(context.Context, history.ConversationID) ([]chat.Message, error) {
	return h.messages, h.err
}

func (h *historyRecorder) Write(_ context.Context, _ history.ConversationID, messages ...chat.Message) (history.WriteOutcome, error) {
	h.messages = messages
	return history.WriteOutcome{Accepted: len(messages)}, h.err
}

func (h *historyRecorder) Clear(context.Context, history.ConversationID) error {
	h.messages = nil
	return h.err
}

func (h *historyRecorder) Conversations(context.Context) ([]history.ConversationID, error) {
	return []history.ConversationID{"one", "two"}, h.err
}

type telemetryRig struct {
	spans  *tracetest.SpanRecorder
	reader *sdkmetric.ManualReader
}

func newHistoryMiddleware(t *testing.T) (historyotel.Middleware, *telemetryRig) {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	reader := sdkmetric.NewManualReader()
	meters := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
		_ = meters.Shutdown(context.Background())
	})
	middleware, err := historyotel.NewMiddleware(historyotel.MiddlewareConfig{
		System:         "  PostgreSQL  ",
		TracerProvider: provider,
		MeterProvider:  meters,
	})
	if err != nil {
		t.Fatal(err)
	}
	return middleware, &telemetryRig{spans: recorder, reader: reader}
}

func TestNewMiddlewareAndDecoratorsPreserveOptionalCapabilities(t *testing.T) {
	if _, err := historyotel.NewMiddleware(historyotel.MiddlewareConfig{}); !errors.Is(err, historyotel.ErrInvalidConfig) {
		t.Fatalf("NewHistory() error = %v", err)
	}
	middleware, _ := newHistoryMiddleware(t)
	var store *historyRecorder
	var lister history.Lister
	if wrapped := middleware.Store(store); wrapped != nil {
		t.Fatalf("Store(typed nil) = %#v", wrapped)
	}
	if wrapped := middleware.Conversations(lister); wrapped != nil {
		t.Fatalf("Conversations(nil) = %#v", wrapped)
	}
	var zero historyotel.Middleware
	if _, err := zero.Store(&historyRecorder{}).Read(t.Context(), "conversation"); !errors.Is(err, historyotel.ErrInvalidConfig) {
		t.Fatalf("zero Middleware Store error = %v, want ErrInvalidConfig", err)
	}
}

func TestStorePreservesResultsAndRecordsOperations(t *testing.T) {
	middleware, rig := newHistoryMiddleware(t)
	wantErr := errors.New("storage failed")
	store := &historyRecorder{err: wantErr}
	wrapped := middleware.Store(store)
	message := chat.NewUserMessage(chat.NewTextPart("hello"))

	if outcome, err := wrapped.Write(t.Context(), "conversation", message); !errors.Is(err, wantErr) || outcome != (history.WriteOutcome{Accepted: 1}) {
		t.Fatalf("Write() error = %v", err)
	}
	if _, err := wrapped.Read(t.Context(), "conversation"); !errors.Is(err, wantErr) {
		t.Fatalf("Read() error = %v", err)
	}
	if err := wrapped.Clear(t.Context(), "conversation"); !errors.Is(err, wantErr) {
		t.Fatalf("Clear() error = %v", err)
	}

	ended := rig.spans.Ended()
	if len(ended) != 3 {
		t.Fatalf("ended spans = %d, want 3", len(ended))
	}
	for index, operation := range []string{"write", "read", "clear"} {
		span := ended[index]
		if span.Name() != "history."+operation || span.SpanKind() != trace.SpanKindClient || span.Status().Code != codes.Error {
			t.Fatalf("span %d name/kind/status = %q/%v/%v", index, span.Name(), span.SpanKind(), span.Status())
		}
		attrs := spanAttributes(t, span)
		assertStringAttr(t, attrs, "db.system.name", "postgresql")
		assertStringAttr(t, attrs, "chat_history.operation.name", operation)
		assertStringAttr(t, attrs, "gen_ai.conversation.id", "conversation")
	}
	metrics := collectMetrics(t, rig.reader)
	assertMetricAttribute(t, metrics, "chat_history.operation.duration", "chat_history.operation.name", "write")
	assertMetricAttribute(t, metrics, "chat_history.operation.duration", "error.type", "*errors.errorString")
	assertMetricLacksAttribute(t, metrics, "chat_history.operation.duration", "gen_ai.conversation.id")
}

func TestListerRecordsResultCount(t *testing.T) {
	middleware, rig := newHistoryMiddleware(t)
	lister := middleware.Conversations(&historyRecorder{})
	ids, err := lister.Conversations(t.Context())
	if err != nil || len(ids) != 2 {
		t.Fatalf("Conversations() = %v, %v", ids, err)
	}
	attrs := spanAttributes(t, rig.spans.Ended()[0])
	if count := attrs["chat_history.conversation.count"].AsInt64(); count != 2 {
		t.Fatalf("conversation count = %d, want 2", count)
	}
}

func TestMetricsClassifyWrappedCancellationByCause(t *testing.T) {
	middleware, rig := newHistoryMiddleware(t)
	want := fmt.Errorf("history interrupted: %w", context.Canceled)
	store := middleware.Store(&historyRecorder{err: want})
	if _, err := store.Read(t.Context(), "conversation"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Read error = %v", err)
	}
	metrics := collectMetrics(t, rig.reader)
	assertMetricAttribute(t, metrics, "chat_history.operation.duration", "error.type", "context.canceled")
}

func spanAttributes(t *testing.T, span sdktrace.ReadOnlySpan) map[string]attribute.Value {
	t.Helper()
	values := make(map[string]attribute.Value, len(span.Attributes()))
	for _, attr := range span.Attributes() {
		values[string(attr.Key)] = attr.Value
	}
	return values
}

func assertStringAttr(t *testing.T, attrs map[string]attribute.Value, key, want string) {
	t.Helper()
	if got := attrs[key].AsString(); got != want {
		t.Fatalf("attribute %s = %q, want %q", key, got, want)
	}
}

func collectMetrics(t *testing.T, reader *sdkmetric.ManualReader) metricdata.ResourceMetrics {
	t.Helper()
	var metrics metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &metrics); err != nil {
		t.Fatal(err)
	}
	return metrics
}

func assertMetricAttribute(
	t *testing.T,
	metrics metricdata.ResourceMetrics,
	name string,
	key attribute.Key,
	want string,
) {
	t.Helper()
	for _, scope := range metrics.ScopeMetrics {
		for _, value := range scope.Metrics {
			if value.Name != name {
				continue
			}
			histogram, ok := value.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("metric %s is %T", name, value.Data)
			}
			for _, point := range histogram.DataPoints {
				if attr, found := point.Attributes.Value(key); found && attr.AsString() == want {
					return
				}
			}
		}
	}
	t.Fatalf("metric %q has no %s=%q datapoint", name, key, want)
}

func assertMetricLacksAttribute(
	t *testing.T,
	metrics metricdata.ResourceMetrics,
	name string,
	key attribute.Key,
) {
	t.Helper()
	for _, scope := range metrics.ScopeMetrics {
		for _, value := range scope.Metrics {
			if value.Name != name {
				continue
			}
			histogram, ok := value.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("metric %s is %T", name, value.Data)
			}
			for _, point := range histogram.DataPoints {
				if _, found := point.Attributes.Value(key); found {
					t.Fatalf("metric %q contains high-cardinality attribute %s", name, key)
				}
			}
			return
		}
	}
	t.Fatalf("metric %q not found", name)
}
