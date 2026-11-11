package vectorstore_test

import (
	"errors"
	"slices"
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

func TestDistinctIDsValidatesEveryIdentityAndKeepsFirstOccurrences(t *testing.T) {
	ids, err := vectorstore.DistinctIDs([]string{"b", "a", "b", "c", "a"})
	if err != nil || !slices.Equal(ids, []string{"b", "a", "c"}) {
		t.Fatalf("DistinctIDs = %v, %v; want first occurrences", ids, err)
	}
	for _, test := range []struct {
		ids  []string
		want error
	}{
		{ids: []string{"a", ""}, want: vectorstore.ErrMissingDocumentID},
		{ids: []string{"\t"}, want: vectorstore.ErrMissingDocumentID},
		{ids: []string{"a", "\xff"}, want: vectorstore.ErrInvalidDocument},
	} {
		if _, err := vectorstore.DistinctIDs(test.ids); !errors.Is(err, test.want) || !errors.Is(err, vectorstore.ErrInvalidRequest) {
			t.Fatalf("DistinctIDs(%q) = %v, want %v within an invalid request", test.ids, err, test.want)
		}
	}
}
