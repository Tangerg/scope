package opensearch

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

	opensearchsdk "github.com/opensearch-project/opensearch-go/v5"

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

// fixtureField is the knn_vector mapping the native index reports.
type fixtureField struct {
	Type             string        `json:"type"`
	Dimensions       int           `json:"dimension"`
	SpaceType        string        `json:"space_type,omitzero"`
	DataType         string        `json:"data_type,omitzero"`
	ModelID          string        `json:"model_id,omitzero"`
	CompressionLevel string        `json:"compression_level,omitzero"`
	Method           *nativeMethod `json:"method,omitzero"`
}

// nativeIndex serves the shared Elastic-protocol index with OpenSearch's
// mapping, settings, kNN clause and client probes through the native SDK.
type nativeIndex struct {
	*elastictest.Index
	t      *testing.T
	client *opensearchsdk.Client
}

func newNativeIndex(t *testing.T) *nativeIndex {
	t.Helper()
	index := elastictest.NewIndex(t, fixtureSpace, elastictest.Engine{
		Embedding: fixtureField{Type: "knn_vector", Dimensions: 2, Method: &nativeMethod{Name: "hnsw", Engine: engineLucene, SpaceType: string(simCosine)}},
		Defaults: map[string]string{
			knnSetting: settingEnabled, derivedSourceSetting: "false", knnDerivedSourceSetting: "false",
			searchDefaultPipelineSetting: pipelineNone, defaultPipelineSetting: pipelineNone, finalPipelineSetting: pipelineNone,
		},
		Neighbors: nativeNeighbors,
		Respond:   nativeHealthResponse,
	})
	server := httptest.NewServer(index)
	t.Cleanup(server.Close)
	client, err := opensearchsdk.NewClient(opensearchsdk.Config{Addresses: []string{server.URL}, Transport: server.Client().Transport, DisableRetry: true, DiscoverNodesOnStart: new(false)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	})
	return &nativeIndex{Index: index, t: t, client: client}
}

func nativeNeighbors(body []byte) ([]string, bool, error) {
	var search searchSyntax
	if err := jsonv2.Unmarshal(body, &search); err != nil || search.Query == nil {
		return nil, false, err
	}
	knn := search.Query.KNN[elastic.EmbeddingField]
	if knn == nil {
		return nil, false, errors.New("opensearch fixture: kNN clause does not name the embedding field")
	}
	if knn.Filter == nil {
		return nil, true, nil
	}
	return knn.Filter.IDs.Values, true, nil
}

func (n *nativeIndex) embedding() fixtureField {
	return n.Mapping.Properties[elastic.EmbeddingField].(fixtureField)
}

// setEmbedding changes a copy of the embedding mapping, including its method.
func (n *nativeIndex) setEmbedding(change func(*fixtureField)) {
	field := n.embedding()
	if field.Method != nil {
		field.Method = new(*field.Method)
	}
	change(&field)
	n.Mapping.Properties[elastic.EmbeddingField] = field
}

func (n *nativeIndex) store() *elastic.Store {
	n.t.Helper()
	store, err := NewStore(n.t.Context(), StoreConfig{Client: n.client, IndexName: elastictest.IndexName, Space: fixtureSpace})
	if err != nil {
		n.t.Fatal(err)
	}
	return store
}

func nativeHealthResponse(request *http.Request) (any, bool) {
	if request.Method != http.MethodGet {
		return nil, false
	}
	switch request.URL.Path {
	case "/":
		return map[string]any{"name": "fixture", "cluster_name": "fixture", "version": map[string]string{"number": "3.6.0", "distribution": "opensearch"}}, true
	case "/_cluster/health":
		if request.URL.RawQuery != "local=true" {
			return nil, false
		}
		return map[string]any{"cluster_name": "fixture", "status": "green", "timed_out": false, "number_of_nodes": 1, "number_of_data_nodes": 1, "active_primary_shards": 1, "active_shards": 1, "relocating_shards": 0, "initializing_shards": 0, "unassigned_shards": 0}, true
	case "/_nodes/_local/http,os,thread_pool":
		return map[string]any{"nodes": map[string]any{"fixture": map[string]any{"name": "fixture", "os": map[string]int{"allocated_processors": 2}}}}, true
	default:
		return nil, false
	}
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

func TestEngineRejectsVectorsOpenSearchWouldRefuse(t *testing.T) {
	for _, fault := range []string{"dimension", "zero vector", "FP16"} {
		t.Run(fault, func(t *testing.T) {
			index := newNativeIndex(t)
			if fault == "FP16" {
				index.setEmbedding(func(field *fixtureField) {
					field.Method.Engine = engineFaiss
					field.Method.SpaceType = string(simL2)
					field.Method.Parameters.Encoder = &nativeEncoder{Name: "sq"}
				})
			}
			vector := map[string][]float64{"dimension": {1}, "zero vector": {1e-50, 0}, "FP16": {65505, 0}}[fault]
			store := index.store()
			batch := fixtureBatch([]*document.Document{{ID: "one", Text: "text"}, {ID: "two", Text: "text"}})
			batch.Records[1].Values = vector
			if err := store.Upsert(t.Context(), batch); err == nil || index.Writes != 0 {
				t.Fatalf("failure=%v, native writes=%d", err, index.Writes)
			}
		})
	}
}

func TestEngineAdmitsEverySpaceTypeAndRefusesForeignPolicy(t *testing.T) {
	for _, metric := range []nativeSimilarity{simCosine, simL2, simInnerProduct, simL1, simLinf} {
		t.Run(string(metric), func(t *testing.T) {
			index := newNativeIndex(t)
			index.setEmbedding(func(field *fixtureField) {
				field.Method.SpaceType = string(metric)
				if metric == simL1 || metric == simLinf {
					field.Method.Engine = engineNMSLIB
				}
			})
			index.store()
			embedding, err := jsonv2.Marshal(index.embedding())
			if err != nil {
				t.Fatal(err)
			}
			schema, err := engine{}.Admit(embedding, index.Settings)
			if err != nil || schema.(nativeSchema).similarity != metric || schema.(nativeSchema).dimensions != 2 {
				t.Fatalf("schema=%#v, error=%v", schema, err)
			}
		})
	}
	for _, fault := range []string{"byte vectors", "no dimension", "native kNN disabled", "derived source", "vector source derived", "pipeline", "search pipeline", "missing engine", "trained model", "unsupported engine space"} {
		t.Run(fault, func(t *testing.T) {
			index := newNativeIndex(t)
			switch fault {
			case "byte vectors":
				index.setEmbedding(func(field *fixtureField) { field.DataType = "byte" })
			case "no dimension":
				index.setEmbedding(func(field *fixtureField) { field.Dimensions = 0 })
			case "native kNN disabled":
				index.Settings.Settings[knnSetting] = "false"
			case "derived source":
				index.Settings.Settings[derivedSourceSetting] = "true"
			case "vector source derived":
				index.Settings.Settings[knnDerivedSourceSetting] = "true"
			case "pipeline":
				index.Settings.Settings[finalPipelineSetting] = "rewrite"
			case "search pipeline":
				index.Settings.Settings[searchDefaultPipelineSetting] = "rewrite"
			case "missing engine":
				index.setEmbedding(func(field *fixtureField) { field.Method.Engine = "" })
			case "trained model":
				index.setEmbedding(func(field *fixtureField) { field.ModelID = "trained" })
			case "unsupported engine space":
				index.setEmbedding(func(field *fixtureField) { field.Method.SpaceType = string(simL1) })
			}
			store, err := NewStore(t.Context(), StoreConfig{Client: index.client, IndexName: elastictest.IndexName, Space: fixtureSpace})
			if store != nil || !errors.Is(err, elastic.ErrIncompatibleIndex) {
				t.Fatalf("incompatible policy accepted: %#v, %v", store, err)
			}
		})
	}
}

func TestNativeInnerProductScoreAndRawRanking(t *testing.T) {
	schema := nativeSchema{similarity: simInnerProduct}
	for _, sample := range []struct{ raw, want float64 }{{0.5, 0.2689414213699951}, {1, 0.5}, {2, 0.7310585786300049}} {
		score, err := schema.Score(sample.raw)
		if err != nil || math.Abs(float64(score)-sample.want) > 1e-15 {
			t.Fatalf("score(%g)=%g, %v", sample.raw, score, err)
		}
	}
	index := newNativeIndex(t)
	index.setEmbedding(func(field *fixtureField) { field.Method.SpaceType = string(simInnerProduct) })
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

func fixtureProtocol(t *testing.T, client APIClient, limit int64) *elastic.Client {
	t.Helper()
	protocol, err := elastic.NewClient(elastic.ClientConfig{Perform: client.Stream, MaxResponseBytes: limit})
	if err != nil {
		t.Fatal(err)
	}
	return protocol
}
