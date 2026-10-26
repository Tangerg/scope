package chat_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/rag"
	ragchat "github.com/Tangerg/scope/rag/chat"
)

func TestModelBackedStagesHonorCanceledModelCalls(t *testing.T) {
	for _, before := range []bool{false, true} {
		for _, name := range []string{"compression", "rewrite", "translation", "expansion"} {
			t.Run(fmt.Sprintf("%s/before=%t", name, before), func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				calls := 0
				model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
					calls++
					cancel()
					if name == "expansion" {
						return textResponse(`{"queries":["rewritten"]}`), nil
					}
					return textResponse("rewritten"), nil
				})
				if before {
					cancel()
				}
				query := mustQuery(t, "question")
				var transform rag.Transformer
				var err error
				switch name {
				case "compression":
					transform, err = ragchat.NewCompressionTransformer(ragchat.CompressionTransformerConfig{Model: model})
				case "rewrite":
					transform, err = ragchat.NewRewriteTransformer(ragchat.RewriteTransformerConfig{Model: model, TargetSearchSystem: "documents"})
				case "translation":
					transform, err = ragchat.NewTranslationTransformer(ragchat.TranslationTransformerConfig{Model: model, TargetLanguage: "English"})
				case "expansion":
					var expander *ragchat.MultiQueryExpander
					expander, err = ragchat.NewMultiQueryExpander(ragchat.MultiQueryExpanderConfig{Model: model, NumberOfQueries: 1})
					if err == nil {
						var result []rag.Query
						result, err = expander.Expand(ctx, query)
						if result != nil {
							t.Fatalf("canceled expansion returned %v", result)
						}
					}
				}
				if transform != nil && err == nil {
					var result rag.Query
					result, err = transform.Transform(ctx, query)
					if result.Text() != "" {
						t.Fatalf("canceled transformation returned %q", result.Text())
					}
				}
				if !errors.Is(err, context.Canceled) || before && calls != 0 || !before && calls != 1 {
					t.Fatalf("error=%v model calls=%d before=%t", err, calls, before)
				}
			})
		}
	}
}

func TestChatRerankerStopsFormattingAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	formatted, calls := 0, 0
	reranker, err := ragchat.NewReranker(ragchat.RerankerConfig{
		Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
			calls++
			return textResponse(`{"scores":[{"index":0,"score":0.9},{"index":1,"score":0.8}]}`), nil
		}),
		Formatter: document.FormatterFunc(func(doc *document.Document) (string, error) {
			formatted++
			cancel()
			return doc.Text, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := reranker.Refine(ctx, mustQuery(t, "query"), rag.Candidates{
		{Document: &document.Document{Text: "first"}}, {Document: &document.Document{Text: "second"}},
	})
	if result != nil || !errors.Is(err, context.Canceled) || formatted != 1 || calls != 0 {
		t.Fatalf("Refine = %v, %v; formatted=%d calls=%d", result, err, formatted, calls)
	}
}
