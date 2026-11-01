package vectorstore_test

import (
	"errors"

	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
)

func TestValidateDocuments(t *testing.T) {
	valid := func(id string) *document.Document {
		return &document.Document{ID: id, Text: "content"}
	}
	tests := []struct {
		name      string
		documents []*document.Document
		want      error
	}{
		{name: "empty", want: vectorstore.ErrEmptyDocuments},
		{name: "nil document", documents: []*document.Document{nil}, want: vectorstore.ErrInvalidDocument},
		{name: "missing content", documents: []*document.Document{{ID: "one"}}, want: vectorstore.ErrInvalidDocument},
		{name: "missing ID", documents: []*document.Document{{Text: "content"}}, want: vectorstore.ErrMissingDocumentID},
		{name: "blank ID", documents: []*document.Document{{ID: "  ", Text: "content"}}, want: vectorstore.ErrMissingDocumentID},
		{name: "duplicate ID", documents: []*document.Document{valid("one"), valid("one")}, want: vectorstore.ErrDuplicateDocumentID},
		{name: "valid", documents: []*document.Document{valid("one"), valid("two")}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := vectorstore.ValidateDocuments(test.documents); !errors.Is(err, test.want) {
				t.Fatalf("ValidateDocuments error = %v, want %v", err, test.want)
			}
		})
	}
}
