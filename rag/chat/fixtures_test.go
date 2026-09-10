package chat_test

import (
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/rag"
)

func candidate(doc *document.Document, score ...float64) rag.Candidate {
	var value float64
	if len(score) > 0 {
		value = score[0]
	}
	return rag.Candidate{Document: doc, Score: rag.Score(value)}
}

func mustQuery(t *testing.T, text string) rag.Query {
	t.Helper()
	q, err := rag.NewQuery(text)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func identifiedDocument(t *testing.T, id, text string) *document.Document {
	t.Helper()
	doc, err := document.NewDocument(text, nil)
	if err != nil {
		t.Fatal(err)
	}
	doc.ID = id
	return doc
}
