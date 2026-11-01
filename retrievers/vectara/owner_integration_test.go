//go:build integration

package vectara

import (
	"context"
	"crypto/rand"
	jsonv2 "encoding/json/v2"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/rag"
)

func liveCorpus(t *testing.T) *Corpus {
	t.Helper()
	endpoint, key := os.Getenv("SCOPE_VECTARA_URL"), os.Getenv("SCOPE_VECTARA_API_KEY")
	if endpoint == "" || key == "" {
		t.Fatal("SCOPE_VECTARA_URL and SCOPE_VECTARA_API_KEY are required with -tags=integration")
	}
	config := Config{Endpoint: endpoint, APIKey: key, CorpusKey: "scope_native_" + rand.Text(), HTTPClient: &http.Client{Timeout: 30 * time.Second}}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	host := &Corpus{endpoint: strings.TrimRight(endpoint, "/"), apiKey: key, corpusKey: config.CorpusKey, httpClient: config.HTTPClient, maxResponseBytes: DefaultMaxResponseBytes}
	payload := struct {
		Key  string `json:"key"`
		Name string `json:"name"`
	}{Key: config.CorpusKey, Name: config.CorpusKey}
	raw, err := host.sendJSON(t.Context(), http.MethodPost, "/v2/corpora", payload, http.StatusCreated)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 30*time.Second)
		defer cancel()
		if _, closeErr := host.sendJSON(ctx, http.MethodDelete, "/v2/corpora/"+config.CorpusKey, nil, http.StatusNoContent); closeErr != nil {
			t.Error(closeErr)
		}
	})
	var response struct {
		Key string `json:"key"`
	}
	if err = jsonv2.Unmarshal(raw, &response); err != nil || response.Key != config.CorpusKey {
		t.Fatalf("native corpus creation acknowledgment: %v", err)
	}
	store, err := New(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestLiveCreateRetrieveAndDelete(t *testing.T) {
	corpus := liveCorpus(t)
	ctx := t.Context()
	doc := &document.Document{ID: "scope-sample", Text: "How do orchids grow in a greenhouse?"}
	if err := corpus.CreateDocument(ctx, doc); err != nil {
		t.Fatal(err)
	}
	q, err := rag.NewQuery("greenhouse orchids")
	if err != nil {
		t.Fatal(err)
	}
	results, err := corpus.Retrieve(ctx, q)
	if err != nil || len(results) == 0 {
		t.Fatalf("native ranking: %v %v", results, err)
	}
	id, ok, err := results[0].Document.Metadata.Decode[string](SourceDocumentIDMetadataKey)
	if err != nil || !ok || id != doc.ID {
		t.Fatalf("native source: %q %v", id, err)
	}
	if err = corpus.DeleteDocument(ctx, doc.ID); err != nil {
		t.Fatal(err)
	}
}
