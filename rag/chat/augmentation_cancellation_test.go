package chat_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/rag"
	ragchat "github.com/Tangerg/scope/rag/chat"
)

func TestContextualAugmenterDoesNotPublishAfterFormattingIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	augmenter, err := ragchat.NewContextualAugmenter(ragchat.ContextualAugmenterConfig{
		Formatter: document.FormatterFunc(func(doc *document.Document) (string, error) {
			cancel()
			return doc.Text, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := augmenter.Augment(ctx, mustQuery(t, "question"), rag.Candidates{candidate(&document.Document{Text: "evidence"})})
	text, replace := result.ReplacementText()
	if !errors.Is(err, context.Canceled) || replace || text != "" || result.Citations().Len() != 0 {
		t.Fatalf("Augment = %q, %v, %v", text, replace, err)
	}
}
