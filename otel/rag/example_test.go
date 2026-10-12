package rag_test

import (
	"context"
	"fmt"

	ragotel "github.com/Tangerg/scope/otel/rag"
	corerag "github.com/Tangerg/scope/rag"
)

// ExampleMiddleware_Wrap instruments a Retriever without changing its result.
// Nil providers resolve to the OpenTelemetry globals, so a composition root can
// enable tracing later without touching this wiring.
func ExampleMiddleware_Wrap() {
	middleware, err := ragotel.NewMiddleware(ragotel.MiddlewareConfig{})
	if err != nil {
		panic(err)
	}
	retriever, err := middleware.Wrap(corerag.RetrieverFunc(
		func(context.Context, corerag.Query) (corerag.Candidates, error) {
			return corerag.Candidates{}, nil
		}))
	if err != nil {
		panic(err)
	}
	query, err := corerag.NewQuery("scope documentation")
	if err != nil {
		panic(err)
	}
	candidates, err := retriever.Retrieve(context.Background(), query)
	if err != nil {
		panic(err)
	}
	fmt.Println(len(candidates))
	// Output:
	// 0
}
