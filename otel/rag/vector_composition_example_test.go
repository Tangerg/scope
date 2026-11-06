package rag_test

import (
	"context"
	"errors"
	"fmt"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/inmemory"
	vectorotel "github.com/Tangerg/scope/otel/vectorstore"
	"github.com/Tangerg/scope/rag"
	"github.com/Tangerg/scope/rag/index"
	"github.com/Tangerg/scope/rag/vector"
)

type vectorAdmission interface {
	CheckDocuments(context.Context, vectorstore.SpaceID, []*document.Document) error
	CheckQuery(context.Context, vectorstore.SpaceID, vectorstore.SearchOptions) error
}

// Admission and the observed operations share the same store; embedding the
// narrow contracts adds no validation or independently mutable policy.
type observedVectors struct {
	vectorAdmission
	vectorstore.Upserter
	vectorstore.Querier
}

var (
	_ index.Destination = observedVectors{}
	_ vector.Source     = observedVectors{}
)

func Example_vectorStoreComposition() {
	const space vectorstore.SpaceID = "manuals-v1"
	ctx := context.Background()
	store, err := inmemory.NewStore(ctx, inmemory.StoreConfig{Space: space, Dimensions: 2})
	if err != nil {
		panic(err)
	}
	spans := tracetest.NewSpanRecorder()
	tracer := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	defer func() {
		if shutdownErr := tracer.Shutdown(ctx); shutdownErr != nil {
			panic(shutdownErr)
		}
	}()
	middleware, err := vectorotel.NewMiddleware(vectorotel.MiddlewareConfig{System: "memory", TracerProvider: tracer})
	if err != nil {
		panic(err)
	}
	observed := observedVectors{vectorAdmission: store, Upserter: middleware.Upsert(store), Querier: middleware.Query(store)}

	embeddingCalls := 0
	model := embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
		embeddingCalls++
		outputs := make([]*embedding.Output, len(request.Texts))
		for i := range outputs {
			outputs[i] = &embedding.Output{Embedding: []float64{1, 0}}
		}
		return embedding.NewResponse(outputs, nil)
	})
	encoderFor := func(space vectorstore.SpaceID) *vector.Encoder {
		binding := embeddingclient.Config{Model: model}
		encoder, encoderErr := vector.NewEncoder(vector.EncoderConfig{Space: space, Documents: binding, Queries: binding})
		if encoderErr != nil {
			panic(encoderErr)
		}
		return encoder
	}
	encoder := encoderFor(space)
	writer, err := index.NewWriter(index.WriterConfig{Destination: observed, Encoder: encoder})
	if err != nil {
		panic(err)
	}
	docs := []*document.Document{{ID: "guide", Text: "Scope guide", Provenance: document.NewProvenance("source://guide", []byte("Scope guide"))}}
	if writeErr := writer.Write(ctx, docs); writeErr != nil {
		panic(writeErr)
	}
	retriever, err := vector.NewRetriever(vector.RetrieverConfig{Encoder: encoder, VectorStore: observed})
	if err != nil {
		panic(err)
	}
	query, err := rag.NewQuery("Scope")
	if err != nil {
		panic(err)
	}
	candidates, err := retriever.Retrieve(ctx, query)
	if err != nil {
		panic(err)
	}
	fmt.Println("document:", candidates[0].Document.ID)

	wrongSpace := encoderFor("other-space")
	rejectedWriter, err := index.NewWriter(index.WriterConfig{Destination: observed, Encoder: wrongSpace})
	if err != nil {
		panic(err)
	}
	rejectedRetriever, err := vector.NewRetriever(vector.RetrieverConfig{Encoder: wrongSpace, VectorStore: observed})
	if err != nil {
		panic(err)
	}
	writeErr := rejectedWriter.Write(ctx, docs)
	_, queryErr := rejectedRetriever.Retrieve(ctx, query)
	fmt.Println("admission rejected:", errors.Is(writeErr, vectorstore.ErrIncompatibleSpace), errors.Is(queryErr, vectorstore.ErrIncompatibleSpace))
	fmt.Println("embedding calls:", embeddingCalls)
	for _, span := range spans.Ended() {
		fmt.Println("span:", span.Name())
	}
	// Output:
	// document: guide
	// admission rejected: true true
	// embedding calls: 2
	// span: upsert
	// span: query
}
