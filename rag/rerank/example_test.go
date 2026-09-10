package rerank_test

import (
	"context"
	"fmt"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/rerank"
	"github.com/Tangerg/scope/rag"
	ragrerank "github.com/Tangerg/scope/rag/rerank"
)

func ExampleRefiner() {
	model := rerank.ModelFunc(func(context.Context, *rerank.Request) (*rerank.Response, error) {
		return &rerank.Response{Results: []*rerank.Result{{Index: 0, Score: 0.9}}}, nil
	})
	refiner, err := ragrerank.NewRefiner(ragrerank.RefinerConfig{Model: model, TopK: 1})
	if err != nil {
		panic(err)
	}
	query, err := rag.NewQuery("Go design")
	if err != nil {
		panic(err)
	}
	doc, err := document.NewDocument("Go favors explicit composition.", nil)
	if err != nil {
		panic(err)
	}
	result, err := refiner.Refine(context.Background(), query, rag.Candidates{{Document: doc}})
	if err != nil {
		panic(err)
	}
	fmt.Println(result[0].Score)
	// Output: 0.9
}
