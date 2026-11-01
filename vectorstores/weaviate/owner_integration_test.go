//go:build integration

package weaviate

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"net"

	"github.com/google/uuid"
	sdk "github.com/weaviate/weaviate-go-client/v6"
	"github.com/weaviate/weaviate-go-client/v6/collections"
	"github.com/weaviate/weaviate-go-client/v6/data"
	"github.com/weaviate/weaviate-go-client/v6/query"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func liveWeaviateClient(t *testing.T) (*Client, *sdk.Client) {
	t.Helper()
	endpoint := os.Getenv("SCOPE_WEAVIATE_URL")
	if endpoint == "" {
		t.Fatal("SCOPE_WEAVIATE_URL is required with -tags=integration")
	}
	address, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	grpcPort := os.Getenv("SCOPE_WEAVIATE_GRPC_PORT")
	if grpcPort == "" {
		grpcPort = "50051"
	}
	client, err := NewClient(t.Context(), ClientConfig{Endpoint: address.String(), GRPCAddress: net.JoinHostPort(address.Hostname(), grpcPort)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := client.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	return client, testAdmin(t, address, grpcPort)
}

func liveWeaviateStore(t *testing.T, metric string) (*Store, *collections.Handle) {
	t.Helper()
	client, admin := liveWeaviateClient(t)
	name := "ScopeNative_" + rand.Text()
	class := nativeClass(name, metric)
	if _, err := admin.Collections.Create(t.Context(), *class); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 30*time.Second)
		defer cancel()
		if err := admin.Collections.Delete(ctx, name); err != nil {
			t.Error(err)
		}
	})
	store, err := NewStore(t.Context(), StoreConfig{Client: client, Collection: name, Space: fixtureSpace})
	if err != nil {
		t.Fatal(err)
	}
	return store, admin.Collections.Use(name)
}

func awaitWeaviateDocuments(ctx context.Context, collection *collections.Handle, docs []*document.Document) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	visibleCount := 0
	for {
		// Approximate nearest-neighbor search is not a complete inventory. Use
		// a bounded non-ranking read of this disposable collection for visibility.
		response, err := collection.Query.OverAll(ctx, query.OverAll{Limit: len(docs), ReturnProperties: []string{fieldContent, fieldMetadata, fieldTerms}})
		if err != nil {
			return fmt.Errorf("fixture visibility: %d of %d hits: %w", visibleCount, len(docs), err)
		}
		visibleCount = len(response.Objects)
		stored := make(map[string]*document.Document, len(response.Objects))
		for _, o := range response.Objects {
			text, textOK := o.Properties[fieldContent].(string)
			facts, factsOK := o.Properties[fieldMetadata].(string)
			terms, termsOK := o.Properties[fieldTerms].([]string)
			if len(o.Properties) != 3 || !textOK || !factsOK || !termsOK {
				return fmt.Errorf("invalid independently read native fields: %v", o.Properties)
			}
			doc, decodeErr := decodeDocument(o.UUID.String(), text, facts, terms)
			if decodeErr != nil {
				return decodeErr
			}
			stored[doc.ID] = doc
		}
		visible := len(stored) == len(docs)
		for _, doc := range docs {
			got := stored[doc.ID]
			visible = visible && got != nil && got.Text == doc.Text && got.Metadata.Equal(doc.Metadata)
		}
		if visible {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("fixture visibility: %d of %d hits: %w", visibleCount, len(docs), ctx.Err())
		case <-ticker.C:
		}
	}
}

func TestLiveNativeFilterAcrossModesAndMetrics(t *testing.T) {
	docs := []*document.Document{{ID: "00000000-0000-4000-8000-000000000001", Text: "native text", Metadata: metadata.Map{"v": json.RawMessage(`"X"`)}}, {ID: "00000000-0000-4000-8000-000000000002", Text: "native text", Metadata: metadata.Map{"v": json.RawMessage(`["X"]`)}}}
	for _, metric := range []string{distanceCosine, distanceDot, distanceL2Squared, distanceHamming, distanceManhattan} {
		t.Run(metric, func(t *testing.T) {
			s, admin := liveWeaviateStore(t, metric)
			if err := s.Upsert(t.Context(), fixtureBatch(docs)); err != nil {
				t.Fatal(err)
			}
			if err := awaitWeaviateDocuments(t.Context(), admin, docs); err != nil {
				t.Fatal(err)
			}
			for _, mode := range []vectorstore.SearchMode{vectorstore.SearchModeSemantic, vectorstore.SearchModeHybrid} {
				q := &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("q"), Options: vectorstore.SearchOptions{Mode: mode, Filter: filter.EQ("v", "X")}}
				r, err := fixtureQuery(t.Context(), s, q)
				if err != nil || len(r.Results) != 1 || r.Results[0].Document.ID != docs[0].ID {
					t.Fatalf("native reply=%v err=%v", r, err)
				}
				q.Options.Filter = filter.EQ("v", 1)
				if r, err = fixtureQuery(t.Context(), s, q); r != nil || !errors.Is(err, vectorstore.ErrUnsupportedFilter) {
					t.Fatalf("unsupported=%v err=%v", r, err)
				}
			}
			if err := s.DeleteIDs(t.Context(), []string{docs[0].ID}); err != nil {
				t.Fatal(err)
			}
			remaining, err := fixtureQuery(t.Context(), s, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("q"), Options: vectorstore.SearchOptions{TopK: 2}})
			if err != nil || len(remaining.Results) != 1 || remaining.Results[0].Document.ID != docs[1].ID {
				t.Fatalf("remaining=%v err=%v", remaining, err)
			}
		})
	}
}
func TestLiveExactMetadata(t *testing.T) {
	store, admin := liveWeaviateStore(t, distanceCosine)
	var docs []*document.Document
	for i, facts := range []metadata.Map{nil, {}, {"huge": json.RawMessage(`1e1000`), "long": json.RawMessage(`1.00000000000000001`), "$native.key": json.RawMessage(`{"nested":[null,{},9007199254740993]}`), "content": json.RawMessage(`"business text"`)}} {
		docs = append(docs, &document.Document{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1), Text: "native text🙂", Metadata: facts})
	}
	if err := store.Upsert(t.Context(), fixtureBatch(docs)); err != nil {
		t.Fatal(err)
	}
	if err := awaitWeaviateDocuments(t.Context(), admin, docs); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []vectorstore.SearchMode{vectorstore.SearchModeSemantic, vectorstore.SearchModeHybrid} {
		response, err := fixtureQuery(t.Context(), store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("native text"), Options: vectorstore.SearchOptions{TopK: len(docs), Mode: mode}})
		if err != nil || len(response.Results) != len(docs) {
			t.Fatalf("response=%v error=%v", response, err)
		}
		for _, doc := range docs {
			if !slices.ContainsFunc(response.Results, func(hit *vectorstore.SearchResult) bool {
				return hit.Document.ID == doc.ID && hit.Document.Text == doc.Text && hit.Document.Metadata.Equal(doc.Metadata) && (hit.Document.Metadata == nil) == (doc.Metadata == nil)
			}) {
				t.Fatalf("native JSON changed: %v", doc)
			}
		}
	}
}

func TestLiveStrictSource(t *testing.T) {
	store, client := liveWeaviateStore(t, distanceCosine)
	first, second := "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000002"
	docs := []*document.Document{{ID: first, Text: "text", Metadata: metadata.Map{"value": json.RawMessage(`"x"`)}}, {ID: second, Text: "text", Metadata: metadata.Map{"value": json.RawMessage(`"x"`)}}}
	if err := store.Upsert(t.Context(), fixtureBatch(docs)); err != nil {
		t.Fatal(err)
	}
	if err := client.Data.Update(t.Context(), data.Object{UUID: new(uuid.MustParse(first)), Properties: map[string]any{fieldMetadata: `{"key":1,"key":2}`}}); err != nil {
		t.Fatal(err)
	}
	response, err := fixtureQuery(t.Context(), store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("q"), Options: vectorstore.SearchOptions{Filter: filter.EQ("value", "x"), MinScore: 1}})
	if err == nil || response != nil {
		t.Fatalf("native corruption became success: %v %v", response, err)
	}
}

func TestLiveRawDistanceGroupsAndSingleHybridFusion(t *testing.T) {
	store, admin := liveWeaviateStore(t, distanceDot)
	var docs []*document.Document
	for i := range 257 {
		text := "other"
		if i == 256 {
			text = "best"
		}
		docs = append(docs, &document.Document{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1), Text: text, Metadata: metadata.Map{"value": json.RawMessage(`"x"`)}})
	}
	batch := fixtureBatch(docs)
	for i, r := range batch.Records {
		batch.Records[i].Values = []float64{40, 0}
		if strings.Contains(r.Document.Text, "best") {
			batch.Records[i].Values = []float64{50, 0}
		}
	}
	if err := store.Upsert(t.Context(), batch); err != nil {
		t.Fatal(err)
	}
	if err := awaitWeaviateDocuments(t.Context(), admin, docs); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []vectorstore.SearchMode{vectorstore.SearchModeSemantic, vectorstore.SearchModeHybrid} {
		response, err := fixtureQuery(t.Context(), store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("q"), Options: vectorstore.SearchOptions{TopK: 1, Filter: filter.EQ("value", "x"), Mode: mode}})
		if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != docs[len(docs)-1].ID {
			t.Fatalf("mode=%s response=%v error=%v", mode, response, err)
		}
		if mode == vectorstore.SearchModeSemantic && response.Results[0].Score != 1 {
			t.Fatalf("expected saturated native dot score: %v", response.Results[0])
		}
	}
}

func TestLiveUpsertSameIdentityReplacesItsSourceAndKeywordProjection(t *testing.T) {
	s, admin := liveWeaviateStore(t, distanceCosine)
	const id = "00000000-0000-4000-8000-000000000017"
	for _, revision := range []string{"first", "second"} {
		doc := &document.Document{ID: id, Text: revision, Metadata: metadata.Map{}, Provenance: document.NewProvenance("urn:test:manual", []byte(revision))}
		if err := doc.Metadata.Set("version", revision); err != nil {
			t.Fatal(err)
		}
		if err := s.Upsert(t.Context(), fixtureBatch([]*document.Document{doc})); err != nil {
			t.Fatal(err)
		}
		if err := awaitWeaviateDocuments(t.Context(), admin, []*document.Document{doc}); err != nil {
			t.Fatal(err)
		}
		hits, err := s.Query(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("q"), Options: vectorstore.SearchOptions{Filter: filter.EQ("version", revision)}})
		if err != nil || len(hits.Results) != 1 {
			t.Fatalf("missing version %s: %v, %v", revision, hits, err)
		}
		got := hits.Results[0].Document
		if got.Text != revision || got.Provenance == nil || got.Provenance.Revision != doc.Provenance.Revision {
			t.Fatalf("stale source after acknowledged upsert: %+v", got)
		}
	}
	old, err := s.Query(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("q"), Options: vectorstore.SearchOptions{Filter: filter.EQ("version", "first")}})
	if err != nil || len(old.Results) != 0 {
		t.Fatalf("obsolete native keyword still selected a document: %v %v", old, err)
	}
}
