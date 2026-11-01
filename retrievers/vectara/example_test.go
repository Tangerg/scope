package vectara_test

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/rag"
	"github.com/Tangerg/scope/retrievers/vectara"
)

func ExampleNew() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	corpus, err := vectara.New(ctx, vectara.Config{APIKey: os.Getenv("VECTARA_API_KEY"), CorpusKey: "manuals", AllowedDocumentIDs: []string{"guide"}, HTTPClient: &http.Client{Timeout: 30 * time.Second}})
	if err != nil {
		panic(err)
	}
	if err = corpus.CreateDocument(ctx, &document.Document{ID: "guide", Text: "Winter care for orchids."}); err != nil {
		panic(err)
	}
	q, err := rag.NewQuery("What care do orchids need in winter?")
	if err != nil {
		panic(err)
	}
	if _, err = corpus.Retrieve(ctx, q); err != nil {
		panic(err)
	}
}
