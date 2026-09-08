package vectorstore_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
)

type batcherFunc func(context.Context, []*document.Document) ([][]*document.Document, error)

func (b batcherFunc) Batch(
	ctx context.Context,
	documents []*document.Document,
) ([][]*document.Document, error) {
	return b(ctx, documents)
}

func TestIndexRequestValidate(t *testing.T) {
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
			request := &vectorstore.IndexRequest{Documents: test.documents}
			if err := request.Validate(); !errors.Is(err, test.want) {
				t.Fatalf("IndexRequest.Validate() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestIndexRequestTextsReturnsAnOwnedOrderedProjection(t *testing.T) {
	request, err := vectorstore.NewIndexRequest([]*document.Document{
		{ID: "a", Text: "first"},
		{ID: "b", Text: "second"},
	})
	if err != nil {
		t.Fatal(err)
	}
	texts, err := request.Texts()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(texts, []string{"first", "second"}) {
		t.Fatalf("Texts() = %v", texts)
	}
	texts[0] = "changed"
	if request.Documents[0].Text != "first" {
		t.Fatalf("Texts() aliases request documents: %#v", request.Documents)
	}
	if _, err := (*vectorstore.IndexRequest)(nil).Texts(); !errors.Is(err, vectorstore.ErrInvalidRequest) {
		t.Fatalf("nil Texts() error = %v", err)
	}
}

func TestIndexRequestBatch(t *testing.T) {
	documents := []*document.Document{{ID: "a", Text: "a"}, {ID: "b", Text: "b"}, {ID: "c", Text: "c"}}
	request, err := vectorstore.NewIndexRequest(documents)
	if err != nil {
		t.Fatal(err)
	}
	batches, err := request.Batch(t.Context(), batcherFunc(func(
		_ context.Context,
		input []*document.Document,
	) ([][]*document.Document, error) {
		return [][]*document.Document{input[:2], input[2:]}, nil
	}))
	if err != nil {
		t.Fatalf("IndexRequest.Batch: %v", err)
	}
	if len(batches) != 2 {
		t.Fatalf("batch count = %d, want 2", len(batches))
	}
}

func TestIndexRequestBatchRejectsNilBatcher(t *testing.T) {
	request := &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "a", Text: "content"}}}
	for _, batcher := range []vectorstore.Batcher{nil, batcherFunc(nil)} {
		batches, err := request.Batch(t.Context(), batcher)
		if err == nil || err.Error() != "vectorstore: batch index request: batcher must not be nil" || batches != nil {
			t.Fatalf("Batch = %v, %v; want nil batcher rejection", batches, err)
		}
	}
}

func TestIndexRequestBatchRejectsInvalidPartitions(t *testing.T) {
	documents := []*document.Document{{ID: "a", Text: "a"}, {ID: "b", Text: "b"}}
	tests := map[string][][]*document.Document{
		"missing document": {documents[:1]},
		"reordered":        {{documents[1], documents[0]}},
		"duplicate":        {{documents[0], documents[0]}},
		"empty batch":      {documents[:1], nil, documents[1:]},
		"extra document":   {documents, {{ID: "c", Text: "c"}}},
	}
	for name, output := range tests {
		t.Run(name, func(t *testing.T) {
			request, err := vectorstore.NewIndexRequest(documents)
			if err != nil {
				t.Fatal(err)
			}
			_, err = request.Batch(t.Context(), batcherFunc(func(
				context.Context,
				[]*document.Document,
			) ([][]*document.Document, error) {
				return output, nil
			}))
			if !errors.Is(err, vectorstore.ErrInvalidBatcherOutput) {
				t.Fatalf("error = %v, want ErrInvalidBatcherOutput", err)
			}
		})
	}
}

func TestIndexRequestBatchPreservesBatcherError(t *testing.T) {
	want := errors.New("batch failed")
	request, err := vectorstore.NewIndexRequest([]*document.Document{{ID: "a", Text: "a"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = request.Batch(t.Context(), batcherFunc(func(
		context.Context,
		[]*document.Document,
	) ([][]*document.Document, error) {
		return nil, want
	}))
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}
