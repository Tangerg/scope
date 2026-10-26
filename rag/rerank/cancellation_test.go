package rerank_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/document"
	corererank "github.com/Tangerg/scope/core/rerank"
	"github.com/Tangerg/scope/rag"
	ragrerank "github.com/Tangerg/scope/rag/rerank"
)

func TestRefinerStopsAfterCancellationByFormatterOrModel(t *testing.T) {
	for _, phase := range []string{"formatter", "model"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			formatted, modelCalls := 0, 0
			refiner, err := ragrerank.NewRefiner(ragrerank.RefinerConfig{
				Formatter: document.FormatterFunc(func(doc *document.Document) (string, error) {
					formatted++
					if phase == "formatter" {
						cancel()
					}
					return doc.Text, nil
				}),
				Model: corererank.ModelFunc(func(context.Context, *corererank.Request) (*corererank.Response, error) {
					modelCalls++
					cancel()
					return &corererank.Response{Results: []*corererank.Result{{Index: 0, Score: 0.9}, {Index: 1, Score: 0.8}}}, nil
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			candidates := rag.Candidates{{Document: &document.Document{Text: "first"}}, {Document: &document.Document{Text: "second"}}}
			result, err := refiner.Refine(ctx, mustQuery(t, "query"), candidates)
			if result != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("Refine = %v, %v", result, err)
			}
			if phase == "formatter" && (formatted != 1 || modelCalls != 0) {
				t.Fatalf("canceled formatting continued: formatted=%d calls=%d", formatted, modelCalls)
			}
		})
	}
}
