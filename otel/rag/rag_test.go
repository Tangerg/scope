package rag_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/Tangerg/scope/core/document"
	ragotel "github.com/Tangerg/scope/otel/rag"
	corerag "github.com/Tangerg/scope/rag"
)

type testRetriever struct {
	candidates corerag.Candidates
	err        error
	observed   bool
}

func (t *testRetriever) Retrieve(ctx context.Context, _ corerag.Query) (corerag.Candidates, error) {
	t.observed = trace.SpanFromContext(ctx).SpanContext().IsValid()
	return t.candidates, t.err
}

type telemetryRig struct {
	spans  *tracetest.SpanRecorder
	reader *sdkmetric.ManualReader
}

func newRig(t *testing.T) (ragotel.Middleware, *telemetryRig) {
	t.Helper()
	spans := tracetest.NewSpanRecorder()
	traces := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	reader := sdkmetric.NewManualReader()
	meters := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		_ = traces.Shutdown(context.Background())
		_ = meters.Shutdown(context.Background())
	})
	middleware, err := ragotel.NewMiddleware(ragotel.MiddlewareConfig{
		TracerProvider: traces, MeterProvider: meters,
	})
	if err != nil {
		t.Fatal(err)
	}
	return middleware, &telemetryRig{spans: spans, reader: reader}
}

func TestMiddlewareObservesExactRetrievalBoundary(t *testing.T) {
	middleware, rig := newRig(t)
	doc, err := document.NewDocument("sensitive document", nil)
	if err != nil {
		t.Fatal(err)
	}
	inner := &testRetriever{candidates: corerag.Candidates{{Document: doc, Score: 0.9}}}
	wrapped, err := middleware.Wrap(inner)
	if err != nil {
		t.Fatal(err)
	}
	query, err := corerag.NewQuery("sensitive query")
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := wrapped.Retrieve(t.Context(), query)
	if err != nil || len(candidates) != 1 || !inner.observed {
		t.Fatalf("Retrieve = (%d, %v), context observed = %t", len(candidates), err, inner.observed)
	}
	span := rig.spans.Ended()[0]
	if span.Name() != "rag.retrieve" || span.SpanKind() != trace.SpanKindInternal {
		t.Fatalf("span = %q/%v", span.Name(), span.SpanKind())
	}
	attributes := attributeMap(span.Attributes())
	assertString(t, attributes, "rag.operation.name", "retrieve")
	if got := attributes["rag.document.count"].AsInt64(); got != 1 {
		t.Fatalf("rag.document.count = %d, want 1", got)
	}
	for _, value := range attributes {
		if value.AsString() == query.Text() || value.AsString() == doc.Text {
			t.Fatal("query or document content leaked into span attributes")
		}
	}
	assertHistogramAttribute(t, durationMetric(t, rig.reader), "rag.operation.name", "retrieve")
}

func TestMiddlewareClassifiesWrappedCancellationWithoutChangingError(t *testing.T) {
	middleware, rig := newRig(t)
	want := fmt.Errorf("retrieval stopped: %w", context.Canceled)
	wrapped, err := middleware.Wrap(&testRetriever{err: want})
	if err != nil {
		t.Fatal(err)
	}
	query, _ := corerag.NewQuery("query")
	if _, gotErr := wrapped.Retrieve(t.Context(), query); !errors.Is(gotErr, context.Canceled) {
		t.Fatalf("Retrieve error = %v", gotErr)
	}
	assertString(t, attributeMap(rig.spans.Ended()[0].Attributes()), "error.type", "context.canceled")
	assertHistogramAttribute(t, durationMetric(t, rig.reader), "error.type", "context.canceled")
}

func TestMiddlewarePreservesRetrieverCancellationPolicy(t *testing.T) {
	middleware, rig := newRig(t)
	want := fmt.Errorf("retrieval canceled before dispatch: %w", context.Canceled)
	inner := &testRetriever{err: want}
	wrapped, err := middleware.Wrap(inner)
	if err != nil {
		t.Fatal(err)
	}
	query, err := corerag.NewQuery("query")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := wrapped.Retrieve(ctx, query); !errors.Is(err, want) || !inner.observed {
		t.Fatalf("Retrieve error = %v, retriever observed = %t", err, inner.observed)
	}
	assertString(t, attributeMap(rig.spans.Ended()[0].Attributes()), "error.type", "context.canceled")
}

func TestMiddlewareRejectsInvalidConstructionAndRetrievers(t *testing.T) {
	var zero ragotel.Middleware
	if wrapped, err := zero.Wrap(&testRetriever{}); wrapped != nil || !errors.Is(err, ragotel.ErrInvalidConfig) {
		t.Fatalf("zero Wrap = (%v, %v)", wrapped, err)
	}
	middleware, _ := newRig(t)
	var typedNil *testRetriever
	for _, candidate := range []corerag.Retriever{nil, typedNil} {
		if wrapped, err := middleware.Wrap(candidate); wrapped != nil || !errors.Is(err, ragotel.ErrInvalidRetriever) {
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
			if metric.Name == "rag.operation.duration" {
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
