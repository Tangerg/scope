package otel_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
	"github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	historyotel "github.com/Tangerg/scope/otel/history"
	ragotel "github.com/Tangerg/scope/otel/rag"
	toolotel "github.com/Tangerg/scope/otel/tool"
	vectorotel "github.com/Tangerg/scope/otel/vectorstore"
	"github.com/Tangerg/scope/rag"
)

func TestCapabilityPanicsEndObservationAndPropagateUnchanged(t *testing.T) {
	for _, operation := range []string{"tool", "retrieve", "read", "write", "clear", "conversations", "index", "search", "delete_ids", "delete_where"} {
		t.Run(operation, func(t *testing.T) {
			spans := tracetest.NewSpanRecorder()
			traces := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(spans))
			reader := sdkmetric.NewManualReader()
			meters := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
			t.Cleanup(func() {
				_ = traces.Shutdown(context.Background())
				_ = meters.Shutdown(context.Background())
			})
			value := &struct{ Secret string }{Secret: "private-panic-payload"}
			capability := panickingCapability{value: value}
			var recovered any
			func() {
				defer func() { recovered = recover() }()
				capability.invoke(t, operation, traces, meters)
			}()
			if recovered != value {
				t.Fatalf("panic = %v, want original pointer", recovered)
			}
			ended := spans.Ended()
			if len(ended) != 1 {
				t.Fatalf("ended spans = %d, want 1 before caller recovery", len(ended))
			}
			span := ended[0]
			attributes := attribute.NewSet(span.Attributes()...)
			classification, _ := attributes.Value("error.type")
			if classification.AsString() != "panic" || span.Status().Code != codes.Error || span.Status().Description != "panic" {
				t.Fatalf("classification/status = %v/%v", classification, span.Status())
			}
			if len(span.Events()) != 1 {
				t.Fatalf("exception events = %d, want 1", len(span.Events()))
			}
			for _, entry := range append(span.Attributes(), span.Events()[0].Attributes...) {
				if strings.Contains(entry.Value.String(), value.Secret) || entry.Key == "exception.message" {
					t.Fatalf("panic value leaked into %s", entry.Key)
				}
			}
			var metrics metricdata.ResourceMetrics
			if err := reader.Collect(t.Context(), &metrics); err != nil {
				t.Fatal(err)
			}
			var count uint64
			for _, scope := range metrics.ScopeMetrics {
				for _, observed := range scope.Metrics {
					if !strings.HasSuffix(observed.Name, ".duration") {
						continue
					}
					for _, point := range observed.Data.(metricdata.Histogram[float64]).DataPoints {
						count += point.Count
						classification, _ := point.Attributes.Value("error.type")
						if classification.AsString() != "panic" {
							t.Fatalf("duration error.type = %q", classification.AsString())
						}
					}
				}
			}
			if count != 1 {
				t.Fatalf("duration records = %d, want 1", count)
			}
		})
	}
}

type panickingCapability struct{ value any }

func (p panickingCapability) Definition() chat.ToolDefinition {
	return chat.ToolDefinition{Name: "panic", InputSchema: json.RawMessage(`{"type":"object"}`)}
}

func (p panickingCapability) Call(context.Context, tool.Invocation) (chat.ToolOutput, error) {
	panic(p.value)
}

func (p panickingCapability) Retrieve(context.Context, rag.Query) (rag.Candidates, error) {
	panic(p.value)
}

func (p panickingCapability) Read(context.Context, history.ConversationID) ([]chat.Message, error) {
	panic(p.value)
}

func (p panickingCapability) Write(context.Context, history.ConversationID, ...chat.Message) (history.WriteOutcome, error) {
	panic(p.value)
}

func (p panickingCapability) Clear(context.Context, history.ConversationID) error { panic(p.value) }

func (p panickingCapability) Conversations(context.Context) ([]history.ConversationID, error) {
	panic(p.value)
}

func (p panickingCapability) Index(context.Context, *vectorstore.IndexRequest) error { panic(p.value) }

func (p panickingCapability) Search(context.Context, *vectorstore.SearchRequest) (*vectorstore.SearchResponse, error) {
	panic(p.value)
}

func (p panickingCapability) DeleteIDs(context.Context, []string) error { panic(p.value) }

func (p panickingCapability) DeleteWhere(context.Context, filter.Predicate) error { panic(p.value) }

func (p panickingCapability) invoke(t *testing.T, operation string, traces trace.TracerProvider, meters metric.MeterProvider) {
	t.Helper()
	switch operation {
	case "tool":
		middleware, err := toolotel.NewMiddleware(toolotel.MiddlewareConfig{TracerProvider: traces, MeterProvider: meters})
		if err != nil {
			t.Fatal(err)
		}
		wrapped, err := middleware.Wrap(p)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = wrapped.Call(t.Context(), tool.Invocation{})
	case "retrieve":
		middleware, err := ragotel.NewMiddleware(ragotel.MiddlewareConfig{TracerProvider: traces, MeterProvider: meters})
		if err != nil {
			t.Fatal(err)
		}
		wrapped, err := middleware.Wrap(p)
		if err != nil {
			t.Fatal(err)
		}
		query, err := rag.NewQuery("private query")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = wrapped.Retrieve(t.Context(), query)
	case "read", "write", "clear", "conversations":
		middleware, err := historyotel.NewMiddleware(historyotel.MiddlewareConfig{System: "memory", TracerProvider: traces, MeterProvider: meters})
		if err != nil {
			t.Fatal(err)
		}
		switch operation {
		case "read":
			_, _ = middleware.Store(p).Read(t.Context(), "conversation")
		case "write":
			_, _ = middleware.Store(p).Write(t.Context(), "conversation")
		case "clear":
			_ = middleware.Store(p).Clear(t.Context(), "conversation")
		case "conversations":
			_, _ = middleware.Conversations(p).Conversations(t.Context())
		}
	case "index", "search", "delete_ids", "delete_where":
		middleware, err := vectorotel.NewMiddleware(vectorotel.MiddlewareConfig{System: "memory", TracerProvider: traces, MeterProvider: meters})
		if err != nil {
			t.Fatal(err)
		}
		switch operation {
		case "index":
			_ = middleware.Index(p).Index(t.Context(), nil)
		case "search":
			_, _ = middleware.Search(p).Search(t.Context(), nil)
		case "delete_ids":
			_ = middleware.DeleteIDs(p).DeleteIDs(t.Context(), nil)
		case "delete_where":
			_ = middleware.DeleteWhere(p).DeleteWhere(t.Context(), nil)
		}
	default:
		t.Fatalf("unknown operation %q", operation)
	}
}
