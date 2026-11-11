package elasticsearch

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/elastic/elastic-transport-go/v8/elastictransport"
	elasticsearchsdk "github.com/elastic/go-elasticsearch/v9"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/vectorstores/protocol/elastic"
	"github.com/Tangerg/scope/vectorstores/protocol/elastic/elastictest"
)

const fixtureSpace vectorstore.SpaceID = "paired-v1"

func fixtureVector(_ string) []float64 { return []float64{1, 0} }

func fixtureBatch(docs []*document.Document) *vectorstore.Batch {
	b := &vectorstore.Batch{Space: fixtureSpace}
	for _, doc := range docs {
		b.Records = append(b.Records, vectorstore.Record{Document: doc, Values: fixtureVector(doc.Text)})
	}
	return b
}

func fixtureBudget() vectorstore.ScanBudget {
	return vectorstore.ScanBudget{MaxRecords: 10000, MaxBytes: 16 << 20, Timeout: time.Minute}
}

func fixtureQuery(ctx context.Context, s *elastic.Store, q *vectorstore.Query) (*vectorstore.SearchResponse, error) {
	if q.Options.Filter != nil {
		return s.QueryByScan(ctx, q, fixtureBudget())
	}
	return s.Query(ctx, q)
}

// fixtureField is the dense_vector mapping the native index reports.
type fixtureField struct {
	Type        string `json:"type"`
	Dimensions  int    `json:"dims"`
	Similarity  string `json:"similarity"`
	ElementType string `json:"element_type"`
	Index       *bool  `json:"index"`
}

// nativeIndex serves the shared Elastic-protocol index with Elasticsearch's
// mapping, settings and kNN clause through the native SDK client.
type nativeIndex struct {
	*elastictest.Index
	t      *testing.T
	client *elasticsearchsdk.Client
}

func newNativeIndex(t *testing.T) *nativeIndex {
	t.Helper()
	index := elastictest.NewIndex(t, fixtureSpace, elastictest.Engine{
		Embedding: fixtureField{Type: "dense_vector", Dimensions: 2, Similarity: "cosine"},
		Defaults:  map[string]string{sourceModeSetting: "STORED", defaultPipelineSetting: pipelineNone, finalPipelineSetting: pipelineNone},
		Neighbors: nativeNeighbors,
		Header:    http.Header{"X-Elastic-Product": {"Elasticsearch"}},
	})
	server := httptest.NewServer(index)
	t.Cleanup(server.Close)
	client, err := elasticsearchsdk.New(elasticsearchsdk.WithAddresses(server.URL), elasticsearchsdk.WithTransportOptions(elastictransport.WithTransport(server.Client().Transport), elastictransport.WithDisableRetry()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		if err := client.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return &nativeIndex{Index: index, t: t, client: client}
}

func nativeNeighbors(body []byte) ([]string, bool, error) {
	var search searchSyntax
	if err := jsonv2.Unmarshal(body, &search); err != nil || search.KNN == nil {
		return nil, false, err
	}
	if search.KNN.Filter == nil {
		return nil, true, nil
	}
	return search.KNN.Filter.IDs.Values, true, nil
}

func (n *nativeIndex) embedding() fixtureField {
	return n.Mapping.Properties[elastic.EmbeddingField].(fixtureField)
}

func (n *nativeIndex) setEmbedding(change func(*fixtureField)) {
	field := n.embedding()
	change(&field)
	n.Mapping.Properties[elastic.EmbeddingField] = field
}

func (n *nativeIndex) store() *elastic.Store {
	n.t.Helper()
	return n.storeThrough(n.client)
}

// storeThrough admits the index through a client that may observe or alter
// the native exchange.
func (n *nativeIndex) storeThrough(client APIClient) *elastic.Store {
	n.t.Helper()
	store, err := NewStore(n.t.Context(), StoreConfig{Client: client, IndexName: elastictest.IndexName, Space: fixtureSpace})
	if err != nil {
		n.t.Fatal(err)
	}
	return store
}

func TestStoreRoundTripsThroughTheNativeClient(t *testing.T) {
	index := newNativeIndex(t)
	store := index.store()
	if err := store.Upsert(t.Context(), fixtureBatch([]*document.Document{{ID: "one", Text: "text"}, {ID: "two", Text: "text"}})); err != nil {
		t.Fatal(err)
	}
	response, err := fixtureQuery(t.Context(), store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query"), Options: vectorstore.SearchOptions{TopK: 1, Filter: filter.IsNull("tenant")}})
	if err != nil || len(response.Results) != 1 || !slices.Equal(index.Groups, []int{2}) {
		t.Fatalf("filtered search = %#v, %v, groups %v", response, err, index.Groups)
	}
	if err := store.DeleteIDs(t.Context(), []string{"one", "two"}); err != nil || len(index.Rows) != 0 {
		t.Fatalf("explicit deletion = %v, rows %v", err, index.Rows)
	}
}

func TestEngineRejectsVectorsElasticsearchWouldRefuse(t *testing.T) {
	for _, fault := range []string{"dimension", "magnitude", "cosine underflow", "dot magnitude"} {
		t.Run(fault, func(t *testing.T) {
			index := newNativeIndex(t)
			if fault == "dot magnitude" {
				index.setEmbedding(func(field *fixtureField) { field.Similarity = "dot_product" })
			}
			vector := map[string][]float64{"dimension": {1}, "magnitude": {1e20, 0}, "cosine underflow": {1e-30, 0}, "dot magnitude": {2, 0}}[fault]
			store := index.store()
			batch := fixtureBatch([]*document.Document{{ID: "one", Text: "text"}, {ID: "two", Text: "text"}})
			batch.Records[1].Values = vector
			if err := store.Upsert(t.Context(), batch); err == nil || index.Writes != 0 {
				t.Fatalf("failure=%v, native writes=%d", err, index.Writes)
			}
		})
	}
}

func TestEngineAdmitsEverySimilarityAndRefusesForeignPolicy(t *testing.T) {
	for _, metric := range []string{"cosine", "l2_norm", "dot_product", "max_inner_product"} {
		t.Run(metric, func(t *testing.T) {
			index := newNativeIndex(t)
			index.setEmbedding(func(field *fixtureField) { field.Similarity = metric })
			index.store()
			embedding, err := jsonv2.Marshal(index.embedding())
			if err != nil {
				t.Fatal(err)
			}
			schema, err := engine{}.Admit(embedding, index.Settings)
			if err != nil || schema != (nativeSchema{dimensions: 2, similarity: nativeSimilarity(metric)}) {
				t.Fatalf("schema=%#v, error=%v", schema, err)
			}
		})
	}
	for _, fault := range []string{"byte vectors", "no dimension", "unindexed", "unknown similarity", "pipeline", "source mode"} {
		t.Run(fault, func(t *testing.T) {
			index := newNativeIndex(t)
			switch fault {
			case "byte vectors":
				index.setEmbedding(func(field *fixtureField) { field.ElementType = "byte" })
			case "no dimension":
				index.setEmbedding(func(field *fixtureField) { field.Dimensions = 0 })
			case "unindexed":
				index.setEmbedding(func(field *fixtureField) { field.Index = new(false) })
			case "unknown similarity":
				index.setEmbedding(func(field *fixtureField) { field.Similarity = "hamming" })
			case "pipeline":
				index.Settings.Settings[finalPipelineSetting] = "rewrite"
			case "source mode":
				index.Settings.Settings[sourceModeSetting] = "synthetic"
			}
			store, err := NewStore(t.Context(), StoreConfig{Client: index.client, IndexName: elastictest.IndexName, Space: fixtureSpace})
			if store != nil || !errors.Is(err, elastic.ErrIncompatibleIndex) {
				t.Fatalf("incompatible policy accepted: %#v, %v", store, err)
			}
		})
	}
}

func TestNativeInnerProductScoreAndRawRanking(t *testing.T) {
	schema := nativeSchema{similarity: simMaxInnerProduct}
	for _, sample := range []struct{ raw, want float64 }{{0.5, 0.2689414213699951}, {1, 0.5}, {2, 0.7310585786300049}} {
		score, err := schema.Score(sample.raw)
		if err != nil || math.Abs(float64(score)-sample.want) > 1e-15 {
			t.Fatalf("score(%g)=%g, %v", sample.raw, score, err)
		}
	}
	index := newNativeIndex(t)
	index.setEmbedding(func(field *fixtureField) { field.Similarity = string(simMaxInnerProduct) })
	store := index.store()
	docs := make([]*document.Document, 513)
	for i := range docs {
		docs[i] = &document.Document{ID: fmt.Sprintf("%04d", i), Text: "text"}
	}
	if err := store.Upsert(t.Context(), fixtureBatch(docs)); err != nil {
		t.Fatal(err)
	}
	// Distinct native scores this large saturate to the same Core score, so
	// only the raw native order can pick the winner across candidate groups.
	for i, doc := range docs {
		index.Scores[doc.ID] = 1e30 + float64(i)*1e29
	}
	response, err := fixtureQuery(t.Context(), store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query"), Options: vectorstore.SearchOptions{TopK: 1, Filter: filter.IsNull("unused")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Document.ID != "0512" || !slices.Equal(index.Groups, []int{512, 1}) {
		t.Fatalf("raw native rank changed: %#v, groups=%v", response, index.Groups)
	}
}
