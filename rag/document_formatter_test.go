package rag_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/media"
	corererank "github.com/Tangerg/scope/core/rerank"
	"github.com/Tangerg/scope/rag"
	ragchat "github.com/Tangerg/scope/rag/chat"
	ragrerank "github.com/Tangerg/scope/rag/rerank"
)

func TestDefaultFormattersRejectMediaWithoutCallingModels(t *testing.T) {
	payload, err := media.NewURI("image/png", "https://example.com/evidence.png")
	if err != nil {
		t.Fatal(err)
	}
	var modelCalls int
	chatModel := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		modelCalls++
		t.Fatal("unsupported media reached chat model")
		return nil, nil
	})
	chatReranker, err := ragchat.NewReranker(ragchat.RerankerConfig{Model: chatModel})
	if err != nil {
		t.Fatal(err)
	}
	reranker, err := ragrerank.NewRefiner(ragrerank.RefinerConfig{
		Model: corererank.ModelFunc(func(context.Context, *corererank.Request) (*corererank.Response, error) {
			t.Fatal("unsupported media reached model")
			return nil, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	augmenter, err := ragchat.NewContextualAugmenter(ragchat.ContextualAugmenterConfig{})
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
	if modelCalls != 0 {
		t.Fatalf("chat model calls = %d", modelCalls)
	}
}

func TestTextFormatterUsesDocumentValidation(t *testing.T) {
	formatter := rag.TextFormatter{}
	for _, doc := range []*document.Document{nil, {}, {Text: string([]byte{0xff})}} {
		if _, err := formatter.Format(doc); !errors.Is(err, document.ErrInvalidDocument) {
			t.Fatalf("Format(%#v) error = %v, want invalid document", doc, err)
		}
	}
	doc := identifiedDocument(t, "evidence", "source text")
	got, err := formatter.Format(doc)
	if err != nil || got != doc.Text {
		t.Fatalf("Format = %q, %v", got, err)
	}
}
