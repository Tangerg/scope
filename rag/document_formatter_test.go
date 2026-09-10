package rag_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/media"
	corererank "github.com/Tangerg/scope/core/rerank"
	"github.com/Tangerg/scope/rag"
)

func TestDefaultFormattersRejectMediaWithoutCallingModels(t *testing.T) {
	payload, err := media.NewURI("image/png", "https://example.com/evidence.png")
	if err != nil {
		t.Fatal(err)
	}
	chatModel := newFakeChatModel(t, "{}")
	chatReranker, err := rag.NewChatReranker(rag.ChatRerankerConfig{Model: chatModel})
	if err != nil {
		t.Fatal(err)
	}
	reranker, err := rag.NewReranker(rag.RerankerConfig{
		Model: corererank.ModelFunc(func(context.Context, *corererank.Request) (*corererank.Response, error) {
			t.Fatal("unsupported media reached model")
			return nil, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	augmenter, err := rag.NewContextualAugmenter(rag.ContextualAugmenterConfig{})
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"", "supporting text"} {
		doc, err := document.NewDocument(text, payload)
		if err != nil {
			t.Fatal(err)
		}
		doc.ID = "evidence"
		candidates := rag.Candidates{candidate(doc)}
		query := mustQuery(t, "query")
		for name, run := range map[string]func() error{
			"chat reranker": func() error { _, err := chatReranker.Refine(t.Context(), query, candidates); return err },
			"reranker":      func() error { _, err := reranker.Refine(t.Context(), query, candidates); return err },
			"augmenter":     func() error { _, err := augmenter.Augment(t.Context(), query, candidates); return err },
		} {
			t.Run(name+"/"+text, func(t *testing.T) {
				if err := run(); !errors.Is(err, rag.ErrUnsupportedMedia) {
					t.Fatalf("error = %v, want ErrUnsupportedMedia", err)
				}
			})
		}
	}
	if chatModel.calls != 0 {
		t.Fatalf("chat model calls = %d", chatModel.calls)
	}
}
