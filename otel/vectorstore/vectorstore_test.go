package vectorstore_test

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	vectorotel "github.com/Tangerg/scope/otel/vectorstore"
)

type indexerFunc func(context.Context, *vectorstore.IndexRequest) error

func (i indexerFunc) Index(ctx context.Context, request *vectorstore.IndexRequest) error {
	return i(ctx, request)
}

type searcherFunc func(context.Context, *vectorstore.SearchRequest) (*vectorstore.SearchResponse, error)

func (s searcherFunc) Search(ctx context.Context, request *vectorstore.SearchRequest) (*vectorstore.SearchResponse, error) {
	return s(ctx, request)
}

type telemetryRig struct {
	spans  *tracetest.SpanRecorder
	reader *sdkmetric.ManualReader
}

func newVectorStoreMiddleware(t *testing.T) (vectorotel.Middleware, *telemetryRig) {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(recorder))
	reader := sdkmetric.NewManualReader()
	meters := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
		_ = meters.Shutdown(context.Background())
	})
	middleware, err := vectorotel.NewMiddleware(vectorotel.MiddlewareConfig{
		System:         "  Qdrant  ",
		Collection:     "knowledge",
		Namespace:      "tenant",
		TracerProvider: provider,
		MeterProvider:  meters,
	})
	if err != nil {
		t.Fatal(err)
	}
	return middleware, &telemetryRig{spans: recorder, reader: reader}
}

func TestNewMiddlewareRequiresSystem(t *testing.T) {
	if _, err := vectorotel.NewMiddleware(vectorotel.MiddlewareConfig{}); !errors.Is(err, vectorotel.ErrInvalidConfig) {
		t.Fatalf("NewVectorStore() error = %v, want ErrInvalidVectorStoreConfig", err)
	}
	var provider *sdktrace.TracerProvider
	if _, err := vectorotel.NewMiddleware(vectorotel.MiddlewareConfig{System: "qdrant", TracerProvider: provider}); err != nil {
		t.Fatalf("typed nil tracer provider must use global default: %v", err)
	}
}

func TestMiddlewareDecoratorsPreserveOptionalCapabilities(t *testing.T) {
	middleware, _ := newVectorStoreMiddleware(t)
	var indexer indexerFunc
	var searcher searcherFunc
	var idDeleter vectorstore.IDDeleter
	var filterDeleter vectorstore.FilterDeleter
	if wrapped := middleware.Index(indexer); wrapped != nil {
		t.Fatalf("Index(typed nil) = %#v", wrapped)
	}
	if wrapped := middleware.Search(searcher); wrapped != nil {
		t.Fatalf("Search(typed nil) = %#v", wrapped)
	}
	if wrapped := middleware.DeleteIDs(idDeleter); wrapped != nil {
		t.Fatalf("DeleteIDs(nil) = %#v", wrapped)
	}
	if wrapped := middleware.DeleteWhere(filterDeleter); wrapped != nil {
		t.Fatalf("DeleteWhere(nil) = %#v", wrapped)
	}
	var zero vectorotel.Middleware
	if err := zero.Index(indexerFunc(func(context.Context, *vectorstore.IndexRequest) error { return nil })).Index(t.Context(), nil); !errors.Is(err, vectorotel.ErrInvalidConfig) {
		t.Fatalf("zero Middleware Index error = %v, want ErrInvalidConfig", err)
	}
}

func TestIndexPreservesNarrowCapabilityAndError(t *testing.T) {
	middleware, rig := newVectorStoreMiddleware(t)
	want := errors.New("write failed")
	var sawSpan bool
	wrapped := middleware.Index(indexerFunc(func(ctx context.Context, request *vectorstore.IndexRequest) error {
		sawSpan = trace.SpanFromContext(ctx).SpanContext().IsValid() && len(request.Documents) == 2
		return want
	}))
	if _, leaked := wrapped.(vectorstore.Searcher); leaked {
		t.Fatal("Indexer decorator leaked Searcher capability")
	}
	err := wrapped.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "one", Text: "one"}, {ID: "two", Text: "two"}}})
	if !errors.Is(err, want) || !sawSpan {
		t.Fatalf("Index() error/span = %v/%t", err, sawSpan)
	}

	if len(rig.spans.Ended()) == 0 {
		t.Fatal("expected a recorded span")
	}
	span := rig.spans.Ended()[0]
	if span.Name() != "index knowledge" || span.SpanKind() != trace.SpanKindClient || span.Status().Code != codes.Error {
		t.Fatalf("span name/kind/status = %q/%v/%v", span.Name(), span.SpanKind(), span.Status())
	}
	attrs := spanAttributes(t, span)
	assertStringAttr(t, attrs, "db.system.name", "qdrant")
	assertStringAttr(t, attrs, "db.operation.name", "index")
	assertStringAttr(t, attrs, "db.collection.name", "knowledge")
	assertStringAttr(t, attrs, "db.namespace", "tenant")
	if got := attrs["db.operation.batch.size"].AsInt64(); got != 2 {
		t.Fatalf("db.operation.batch.size = %d, want 2", got)
	}
	metrics := collectMetrics(t, rig.reader)
	assertMetricAttribute(t, metrics, "db.client.operation.duration", "db.operation.name", "index")
	assertMetricAttribute(t, metrics, "db.client.operation.duration", "error.type", "*errors.errorString")
}

func TestSearchPreservesMatchesAndRecordsCount(t *testing.T) {
	middleware, rig := newVectorStoreMiddleware(t)
	want := &vectorstore.SearchResponse{Results: []*vectorstore.SearchResult{{Document: &document.Document{ID: "one", Text: "one"}, Score: 0.9}}}
	wrapped := middleware.Search(searcherFunc(func(context.Context, *vectorstore.SearchRequest) (*vectorstore.SearchResponse, error) {
		return want, nil
	}))
	if _, leaked := wrapped.(vectorstore.Indexer); leaked {
		t.Fatal("Searcher decorator leaked Indexer capability")
	}
	got, err := wrapped.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1}})
	if err != nil || got != want {
		t.Fatalf("Search() = %#v, %v", got, err)
	}
	if len(rig.spans.Ended()) == 0 {
		t.Fatal("expected a recorded span")
	}
	attrs := spanAttributes(t, rig.spans.Ended()[0])
	if count := attrs["db.response.returned_rows"].AsInt64(); count != 1 {
		t.Fatalf("db.response.returned_rows = %d, want 1", count)
	}
	if topK := attrs["db.vector.query.top_k"].AsInt64(); topK != 1 {
		t.Fatalf("db.vector.query.top_k = %d, want 1", topK)
	}
	if threshold := attrs["db.vector.query.similarity_threshold"].AsFloat64(); threshold != 0 {
		t.Fatalf("db.vector.query.similarity_threshold = %v, want 0", threshold)
	}
	metrics := collectMetrics(t, rig.reader)
	assertMetricAttribute(t, metrics, "db.client.operation.duration", "db.collection.name", "knowledge")
	assertMetricLacksAttribute(t, metrics, "db.client.operation.duration", "db.vector.query.top_k")
	if rows := histogramInt64Sum(t, metrics, "db.vector.search.returned_rows"); rows != 1 {
		t.Fatalf("returned rows metric = %d, want 1", rows)
	}
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

func histogramInt64Sum(t *testing.T, metrics metricdata.ResourceMetrics, name string) int64 {
	t.Helper()
	for _, scope := range metrics.ScopeMetrics {
		for _, value := range scope.Metrics {
			if value.Name != name {
				continue
			}
			histogram, ok := value.Data.(metricdata.Histogram[int64])
			if !ok {
				t.Fatalf("metric %s is %T", name, value.Data)
			}
			var sum int64
			for _, point := range histogram.DataPoints {
				sum += point.Sum
			}
			return sum
		}
	}
	t.Fatalf("metric %q not found", name)
	return 0
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
