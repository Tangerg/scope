package rag_test

import (
	"context"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/codes"

	corerag "github.com/Tangerg/scope/rag"
)

// TestRetrievePanicEndsObservationAndPropagates keeps the retrieval half of the
// cross-capability panic contract inside this module after the split: a
// panicking retriever ends exactly one span classified as a panic, re-panics the
// original value unchanged, and never leaks that value into telemetry.
func TestRetrievePanicEndsObservationAndPropagates(t *testing.T) {
	middleware, rig := newRig(t)
	value := &struct{ Secret string }{Secret: "private-panic-payload"}
	wrapped, err := middleware.Wrap(corerag.RetrieverFunc(func(context.Context, corerag.Query) (corerag.Candidates, error) {
		panic(value)
	}))
	if err != nil {
		t.Fatal(err)
	}
	query, err := corerag.NewQuery("sensitive query")
	if err != nil {
		t.Fatal(err)
	}
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_, _ = wrapped.Retrieve(t.Context(), query)
	}()
	if recovered != value {
		t.Fatalf("panic = %v, want original pointer", recovered)
	}
	ended := rig.spans.Ended()
	if len(ended) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(ended))
	}
	span := ended[0]
	if got := attributeMap(span.Attributes())["error.type"].AsString(); got != "panic" ||
		span.Status().Code != codes.Error || span.Status().Description != "panic" {
		t.Fatalf("classification/status = %q/%v", got, span.Status())
	}
	if len(span.Events()) != 1 {
		t.Fatalf("exception events = %d, want 1", len(span.Events()))
	}
	for _, entry := range append(span.Attributes(), span.Events()[0].Attributes...) {
		if strings.Contains(entry.Value.String(), value.Secret) || entry.Key == "exception.message" {
			t.Fatalf("panic value leaked into %s", entry.Key)
		}
	}
	assertHistogramAttribute(t, durationMetric(t, rig.reader), "error.type", "panic")
}
