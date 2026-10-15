//go:build integration

package pinecone

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	pineconesdk "github.com/pinecone-io/go-pinecone/v7/pinecone"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

// The fixture must implement the SDK's current control and data protocol.
// An older Local control response cannot establish the current native policy.
func localStore(t *testing.T, metric pineconesdk.IndexMetric) (*Store, *pineconesdk.IndexConnection) {
	t.Helper()
	endpoint := os.Getenv("SCOPE_PINECONE_LOCAL_ENDPOINT")
	name := os.Getenv("SCOPE_PINECONE_LOCAL_INDEX_" + strings.ToUpper(string(metric)))
	if endpoint == "" || name == "" {
		t.Fatal("Pinecone Local endpoint and preprovisioned 2-dimensional metric index are required")
	}
	client, err := pineconesdk.NewClient(pineconesdk.NewClientParams{ApiKey: "local", Host: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	index, err := client.DescribeIndex(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}
	var schema nativeSchema
	if err := schema.read(index, name); err != nil || schema.metric != metric || schema.dimensions != 2 {
		t.Fatalf("local fixture policy differs: %v, %v", index, err)
	}
	connection, err := client.Index(pineconesdk.NewIndexConnParams{Host: "http://" + index.Host, Namespace: "scope-test-" + rand.Text()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if deleteErr := connection.DeleteAllVectorsInNamespace(ctx); deleteErr != nil {
			t.Error(deleteErr)
		}
		if closeErr := connection.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	model, err := embeddingclient.New(fixtureModel())
	if err != nil {
		t.Fatal(err)
	}
	return &Store{index: connection, schema: schema, embeddingClient: model, documentBatcher: fixtureBatcher{}}, connection
}

func TestLocalNativeCoreFilterConformance(t *testing.T) {
	for _, metric := range []pineconesdk.IndexMetric{pineconesdk.IndexMetricCosine, pineconesdk.IndexMetricDotproduct, pineconesdk.IndexMetricEuclidean} {
		t.Run(string(metric), func(t *testing.T) {
			storetest.FilterConformance(t, storetest.FilterConfig{Query: func(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
				store, _ := localStore(t, metric)
				if err := store.Index(ctx, &vectorstore.IndexRequest{Documents: docs}); err != nil {
					return nil, err
				}
				request := &vectorstore.SearchRequest{Query: "q", Options: vectorstore.SearchOptions{TopK: len(docs), Filter: predicate}}
				response, err := store.Search(ctx, request)
				if err != nil {
					return nil, err
				}
				return resultIDs(response), nil
			}})
		})
	}
}

func TestLocalNativeExactMetadataAndScores(t *testing.T) {
	for _, metric := range []pineconesdk.IndexMetric{pineconesdk.IndexMetricCosine, pineconesdk.IndexMetricDotproduct, pineconesdk.IndexMetricEuclidean} {
		t.Run(string(metric), func(t *testing.T) {
			store, native := localStore(t, metric)
			var facts metadata.Map
			if err := facts.UnmarshalJSON([]byte(`{"huge":1e1000,"integer":9007199254740993,"$key":{"array":[1,null,true,{}]}}`)); err != nil {
				t.Fatal(err)
			}
			docs := []*document.Document{{ID: "a/b?: #", Text: "text🙂", Metadata: facts}, {ID: "empty", Text: "empty", Metadata: metadata.Map{}}, {ID: "nil", Text: "nil"}}
			installDocuments(t, store, docs...)
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q", Options: vectorstore.SearchOptions{TopK: 3}})
			if err != nil || len(response.Results) != len(docs) {
				t.Fatalf("response=%v error=%v", response, err)
			}
			for _, result := range response.Results {
				original := docs[slices.IndexFunc(docs, func(doc *document.Document) bool { return doc.ID == result.Document.ID })]
				if result.Document.Text != original.Text || !result.Document.Metadata.Equal(original.Metadata) || (result.Document.Metadata == nil) != (original.Metadata == nil) {
					t.Fatalf("native round trip changed %#v", result.Document)
				}
			}
			page, err := native.QueryByVectorValues(t.Context(), &pineconesdk.QueryByVectorValuesRequest{Vector: []float32{1, 0}, TopK: 3, IncludeMetadata: true, IncludeValues: true})
			if err != nil || len(page.Matches) != 3 {
				t.Fatalf("raw query=%v error=%v", page, err)
			}
			for _, hit := range page.Matches {
				score, scoreErr := store.schema.score(float64(hit.Score))
				if scoreErr != nil {
					t.Fatal(scoreErr)
				}
				if !slices.ContainsFunc(response.Results, func(result *vectorstore.SearchResult) bool {
					return result.Document.ID == hit.Vector.Id && result.Score == score
				}) {
					t.Fatalf("native score not preserved: %v", hit)
				}
			}
		})
	}
}

type changedNativeIndex struct {
	indexConnection
	connection  *pineconesdk.IndexConnection
	afterUpdate func(context.Context) error
}

func (c *changedNativeIndex) DeleteVectorsByFilter(ctx context.Context, selection *pineconesdk.MetadataFilter) error {
	if err := c.connection.UpdateVector(ctx, &pineconesdk.UpdateVectorRequest{Id: "changed", Metadata: &structpb.Struct{Fields: map[string]*structpb.Value{metadataField: structpb.NewStringValue(`{"value":"b"}`)}}}); err != nil {
		return err
	}
	if c.afterUpdate != nil {
		if err := c.afterUpdate(ctx); err != nil {
			return err
		}
	}
	return c.connection.DeleteVectorsByFilter(ctx, selection)
}

func TestLocalNativeConditionalDeleteFailureAndStrictSource(t *testing.T) {
	store, native := localStore(t, pineconesdk.IndexMetricCosine)
	facts, err := metadata.FromValues(map[string]any{"value": "a"})
	if err != nil {
		t.Fatal(err)
	}
	installDocuments(t, store, &document.Document{ID: "changed", Text: "text", Metadata: facts}, &document.Document{ID: "unchanged", Text: "text", Metadata: facts})
	store.index = &changedNativeIndex{indexConnection: native, connection: native}
	if err = store.DeleteWhere(t.Context(), filter.EQ("value", "a")); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("Local rejection was suppressed or replaced: %v", err)
	}
	page, err := native.FetchVectors(t.Context(), []string{"changed", "unchanged"})
	if err != nil || len(page.Vectors) != 2 || page.Vectors["changed"] == nil || page.Vectors["unchanged"] == nil || page.Vectors["changed"].Metadata.Fields[metadataField].GetStringValue() != `{"value":"b"}` {
		t.Fatalf("rejected deletion lost records: %v, %v", page, err)
	}
	if err = native.UpdateVector(t.Context(), &pineconesdk.UpdateVectorRequest{Id: "changed", Metadata: &structpb.Struct{Fields: map[string]*structpb.Value{"legacy": structpb.NewStringValue("extra")}}}); err != nil {
		t.Fatal(err)
	}
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q", Options: vectorstore.SearchOptions{Filter: filter.EQ("value", "absent"), MinScore: 1}})
	if err == nil || response != nil {
		t.Fatalf("native corrupt source became an empty result: %v, %v", response, err)
	}
}

func TestLocalNativeMetadataGroupsRetainRanking(t *testing.T) {
	store, native := localStore(t, pineconesdk.IndexMetricDotproduct)
	var docs []*document.Document
	for i := range filterGroupSize + 1 {
		facts, err := metadata.FromValues(map[string]any{"value": "a", "unique": i})
		if err != nil {
			t.Fatal(err)
		}
		docs = append(docs, &document.Document{ID: fmt.Sprintf("id-%03d", i), Text: "text", Metadata: facts})
	}
	installDocuments(t, store, docs...)
	for i, doc := range docs {
		if err := native.UpdateVector(t.Context(), &pineconesdk.UpdateVectorRequest{Id: doc.ID, Values: []float32{float32(i + 40), 0}}); err != nil {
			t.Fatal(err)
		}
	}
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q", Options: vectorstore.SearchOptions{TopK: 1, Filter: filter.EQ("value", "a")}})
	if err != nil || !slices.Equal(resultIDs(response), []string{docs[len(docs)-1].ID}) {
		t.Fatalf("ranking=%v error=%v", response, err)
	}
}
