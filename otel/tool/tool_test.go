package tool_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/Tangerg/scope/core/chat"
	coretool "github.com/Tangerg/scope/core/tool"
	toolotel "github.com/Tangerg/scope/otel/tool"
)

type marked interface{ Marked() }

type testTool struct {
	result string
	err    error
	called bool
}

func (*testTool) Definition() chat.ToolDefinition {
	return chat.ToolDefinition{
		Name: "lookup", Description: "Look up one value.",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}
}

func (t *testTool) Call(ctx context.Context, invocation coretool.Invocation) (chat.ToolOutput, error) {
	t.called = trace.SpanFromContext(ctx).SpanContext().IsValid() && string(invocation.Arguments()) == `{"key":"secret"}`
	return chat.NewTextToolOutput(t.result), t.err
}

func (*testTool) Marked() {}

type telemetryRig struct {
	spans  *tracetest.SpanRecorder
	reader *sdkmetric.ManualReader
}

func newRig(t *testing.T) (toolotel.Middleware, *telemetryRig) {
	t.Helper()
	spans := tracetest.NewSpanRecorder()
	traces := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	reader := sdkmetric.NewManualReader()
	meters := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		_ = traces.Shutdown(context.Background())
		_ = meters.Shutdown(context.Background())
	})
	middleware, err := toolotel.NewMiddleware(toolotel.MiddlewareConfig{
		TracerProvider: traces, MeterProvider: meters,
	})
	if err != nil {
		t.Fatal(err)
	}
	return middleware, &telemetryRig{spans: spans, reader: reader}
}

func TestMiddlewareTracesAndMeasuresExactToolBoundary(t *testing.T) {
	middleware, rig := newRig(t)
	inner := &testTool{result: "sensitive-result"}
	wrapped, err := middleware.Wrap(inner)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := coretool.Bind(wrapped)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := binding.Prepare(chat.ToolCall{ID: "test", Name: "lookup", Arguments: `{"key":"secret"}`})
	if err != nil {
		t.Fatal(err)
	}
	result, err := binding.Call(t.Context(), invocation)
	text, textOK := result.Text()
	if err != nil || !textOK || text != inner.result || !inner.called {
		t.Fatalf("Call = (%q, %v), context observed = %t", text, err, inner.called)
	}
	if _, found, err := coretool.Capability[marked](wrapped); err != nil || !found {
		t.Fatalf("wrapped capability = found:%t error:%v", found, err)
	}
	span := rig.spans.Ended()[0]
	if span.Name() != "execute_tool lookup" || span.SpanKind() != trace.SpanKindInternal {
		t.Fatalf("span = %q/%v", span.Name(), span.SpanKind())
	}
	attributes := attributeMap(span.Attributes())
	assertString(t, attributes, "gen_ai.operation.name", "execute_tool")
	assertString(t, attributes, "gen_ai.tool.name", "lookup")
	assertString(t, attributes, "gen_ai.tool.type", "function")
	for _, value := range attributes {
		if value.AsString() == `{"key":"secret"}` || value.AsString() == inner.result {
			t.Fatal("tool arguments or result leaked into span attributes")
		}
	}
	metric := durationMetric(t, rig.reader)
	assertHistogramAttribute(t, metric, "gen_ai.tool.name", "lookup")
	data := metric.Data.(metricdata.Histogram[float64])
	point := data.DataPoints[0]
	if point.Sum != span.EndTime().Sub(span.StartTime()).Seconds() {
		t.Errorf("metric duration = %v, span duration = %v", point.Sum, span.EndTime().Sub(span.StartTime()).Seconds())
	}
	if !slices.Equal(point.Bounds, []float64{0.01, 0.02, 0.04, 0.08, 0.16, 0.32, 0.64, 1.28, 2.56, 5.12, 10.24, 20.48, 40.96, 81.92}) {
		t.Errorf("duration bounds = %v", point.Bounds)
	}
}

func TestMiddlewareClassifiesWrappedCancellationWithoutChangingError(t *testing.T) {
	middleware, rig := newRig(t)
	want := fmt.Errorf("tool stopped: %w", context.Canceled)
	wrapped, err := middleware.Wrap(&testTool{err: want})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := coretool.Bind(wrapped)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := binding.Prepare(chat.ToolCall{ID: "test", Name: "lookup", Arguments: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	if _, gotErr := binding.Call(t.Context(), invocation); !errors.Is(gotErr, context.Canceled) {
		t.Fatalf("Call error = %v", gotErr)
	}
	span := rig.spans.Ended()[0]
	assertString(t, attributeMap(span.Attributes()), "error.type", "context.canceled")
	assertHistogramAttribute(t, durationMetric(t, rig.reader), "error.type", "context.canceled")
}

func TestMiddlewareRejectsNilAndInvalidTools(t *testing.T) {
	var zero toolotel.Middleware
	if wrapped, err := zero.Wrap(&testTool{}); wrapped != nil || !errors.Is(err, toolotel.ErrInvalidConfig) {
		t.Fatalf("zero Wrap = (%v, %v)", wrapped, err)
	}
	middleware, _ := newRig(t)
	var typedNil *testTool
	for _, candidate := range []coretool.Tool{nil, typedNil} {
		if wrapped, err := middleware.Wrap(candidate); wrapped != nil || !errors.Is(err, toolotel.ErrInvalidTool) {
			t.Fatalf("Wrap(%T) = (%v, %v)", candidate, wrapped, err)
		}
	}
}

func durationMetric(t *testing.T, reader *sdkmetric.ManualReader) metricdata.Metrics {
	t.Helper()
	var metrics metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &metrics); err != nil {
		t.Fatal(err)
	}
	for _, scope := range metrics.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if metric.Name == "gen_ai.execute_tool.duration" {
				return metric
			}
		}
	}
	t.Fatal("duration metric is missing")
	return metricdata.Metrics{}
}

func assertHistogramAttribute(t *testing.T, metric metricdata.Metrics, key, want string) {
	t.Helper()
	data, ok := metric.Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("metric data = %T", metric.Data)
	}
	for _, point := range data.DataPoints {
		value, found := point.Attributes.Value(attribute.Key(key))
		if found && value.AsString() == want {
			return
		}
	}
	t.Fatalf("metric attribute %s=%q is missing", key, want)
}

func attributeMap(values []attribute.KeyValue) map[string]attribute.Value {
	result := make(map[string]attribute.Value, len(values))
	for _, value := range values {
		result[string(value.Key)] = value.Value
	}
	return result
}

func assertString(t *testing.T, values map[string]attribute.Value, key, want string) {
	t.Helper()
	if got := values[key].AsString(); got != want {
		t.Fatalf("attribute %s = %q, want %q", key, got, want)
	}
}
