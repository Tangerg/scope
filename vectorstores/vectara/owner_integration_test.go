//go:build integration

package vectara

import (
	"context"
	"crypto/rand"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

func liveVectaraStore(t *testing.T) *Store {
	t.Helper()
	endpoint, key := os.Getenv("SCOPE_VECTARA_URL"), os.Getenv("SCOPE_VECTARA_API_KEY")
	if endpoint == "" || key == "" {
		t.Fatal("SCOPE_VECTARA_URL and SCOPE_VECTARA_API_KEY are required with -tags=integration")
	}
	config := StoreConfig{Endpoint: endpoint, APIKey: key, CorpusKey: "scope_native_" + rand.Text(), DocumentBatcher: testBatcher{}, HTTPClient: &http.Client{Timeout: 30 * time.Second}}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	host := &Store{endpoint: strings.TrimRight(endpoint, "/"), apiKey: key, corpusKey: config.CorpusKey, httpClient: config.HTTPClient, maxResponseBytes: DefaultMaxResponseBytes}
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
	store, err := NewStore(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestLiveFilterConformance(t *testing.T) {
	store := liveVectaraStore(t)
	var previous []string
	storetest.FilterConformance(t, storetest.FilterConfig{Query: func(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
		if err := store.DeleteIDs(ctx, previous); err != nil {
			return nil, err
		}
		previous = nil
		for _, doc := range docs {
			previous = append(previous, doc.ID)
		}
		if err := store.Index(ctx, &vectorstore.IndexRequest{Documents: docs}); err != nil {
			return nil, err
		}
		response, err := store.Search(ctx, &vectorstore.SearchRequest{Query: "filter conformance", Options: vectorstore.SearchOptions{TopK: len(docs), Filter: predicate}})
		if err != nil {
			return nil, err
		}
		var ids []string
		for _, hit := range response.Results {
			ids = append(ids, hit.Document.ID)
		}
		return ids, nil
	}})
}

func TestLiveCompleteSourceAndNativeReplacement(t *testing.T) {
	store := liveVectaraStore(t)
	var docs []*document.Document
	for i, facts := range []metadata.Map{nil, {}, {"huge": json.RawMessage(`1e1000`), "exact": json.RawMessage(`1.00000000000000001`), "$business.key": json.RawMessage(`{"nested":[null,{},9007199254740993]}`)}} {
		docs = append(docs, &document.Document{ID: fmt.Sprint(i), Text: "current complete source🙂", Metadata: facts})
	}
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs}); err != nil {
		t.Fatal(err)
	}
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "current complete source", Options: vectorstore.SearchOptions{TopK: len(docs)}})
	if err != nil || len(response.Results) != len(docs) {
		t.Fatalf("response=%v error=%v", response, err)
	}
	for _, doc := range docs {
		found := false
		for _, hit := range response.Results {
			if hit.Document.ID == doc.ID && hit.Document.Text == doc.Text && hit.Document.Metadata.Equal(doc.Metadata) && (hit.Document.Metadata == nil) == (doc.Metadata == nil) {
				found = true
			}
		}
		if !found {
			t.Fatalf("native metadata changed: %v", doc)
		}
	}
	docs[0].Text = "native replacement"
	if err = store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs[:1]}); err != nil {
		t.Fatal(err)
	}
	raw, err := store.sendJSON(t.Context(), http.MethodGet, store.documentPath(docs[0].ID), nil, http.StatusOK)
	if err != nil {
		t.Fatal(err)
	}
	var record nativeDocument
	if err = jsonv2.Unmarshal(raw, &record); err != nil || len(record.Parts) != 1 || record.Parts[0].Text != docs[0].Text {
		t.Fatalf("native replacement did not preserve identity: %v", err)
	}
	var ids []string
	for _, doc := range docs {
		ids = append(ids, doc.ID)
	}
	if err = store.DeleteIDs(t.Context(), ids); err != nil {
		t.Fatal(err)
	}
	if source, err := store.selectDocuments(t.Context(), nil); err != nil || len(source) != 0 {
		t.Fatalf("native ID deletion retained source: %v %v", source, err)
	}
}
