//go:build integration

package azurecosmos

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

func TestNativeCosmos(t *testing.T) {
	endpoint, key, database := os.Getenv("SCOPE_COSMOS_ENDPOINT"), os.Getenv("SCOPE_COSMOS_KEY"), os.Getenv("SCOPE_COSMOS_DATABASE")
	if endpoint == "" || key == "" || database == "" {
		t.Fatal("SCOPE_COSMOS_ENDPOINT, SCOPE_COSMOS_KEY and SCOPE_COSMOS_DATABASE are required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	httpClient := &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	credential, err := azcosmos.NewKeyCredential(key)
	if err != nil {
		t.Fatal(err)
	}
	client, err := azcosmos.NewClientWithKey(endpoint, credential, &azcosmos.ClientOptions{ClientOptions: azcore.ClientOptions{Transport: httpClient}})
	if err != nil {
		t.Fatal(err)
	}
	db, err := client.NewDatabase(database)
	if err != nil {
		t.Fatal(err)
	}
	for _, function := range []azcosmos.VectorDistanceFunction{azcosmos.VectorDistanceFunctionCosine, azcosmos.VectorDistanceFunctionDotProduct, azcosmos.VectorDistanceFunctionEuclidean} {
		t.Run(string(function), func(t *testing.T) {
			name := "scope-test-" + strings.ToLower(rand.Text())
			properties := azcosmos.ContainerProperties{ID: name, PartitionKeyDefinition: azcosmos.PartitionKeyDefinition{Kind: azcosmos.PartitionKeyKindHash, Paths: []string{"/partition_key"}, Version: 2}, VectorEmbeddingPolicy: &azcosmos.VectorEmbeddingPolicy{VectorEmbeddings: []azcosmos.VectorEmbedding{{Path: "/embedding", DataType: azcosmos.VectorDataTypeFloat32, DistanceFunction: function, Dimensions: 2}}}, IndexingPolicy: &azcosmos.IndexingPolicy{Automatic: true, IndexingMode: azcosmos.IndexingModeConsistent, IncludedPaths: []azcosmos.IncludedPath{{Path: "/*"}}, ExcludedPaths: []azcosmos.ExcludedPath{{Path: "/embedding/*"}, {Path: "/metadata/?"}}, VectorIndexes: []azcosmos.VectorIndex{{Path: "/embedding", Type: azcosmos.VectorIndexTypeFlat}}}}
			throughput := azcosmos.NewManualThroughputProperties(400)
			if _, createErr := db.CreateContainer(ctx, properties, &azcosmos.CreateContainerOptions{ThroughputProperties: &throughput}); createErr != nil {
				t.Fatal(createErr)
			}
			container, containerErr := client.NewContainer(database, name)
			if containerErr != nil {
				t.Fatal(containerErr)
			}
			t.Cleanup(func() {
				cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
				defer stop()
				if _, deleteErr := container.Delete(cleanup, nil); deleteErr != nil {
					t.Error(deleteErr)
				}
			})
			store, storeErr := NewStore(ctx, StoreConfig{Container: container, PartitionKey: "library", EmbeddingModel: constantModel(), DocumentBatcher: testBatcher{size: 32}})
			if storeErr != nil {
				t.Fatal(storeErr)
			}
			storetest.FilterConformance(t, storetest.FilterConfig{Query: func(queryCtx context.Context, docs []*document.Document, predicate filter.Predicate) (ids []string, queryErr error) {
				defer func() {
					cleanup, stop := context.WithTimeout(context.WithoutCancel(queryCtx), time.Minute)
					defer stop()
					queryErr = errors.Join(queryErr, store.DeleteWhere(cleanup, filter.IsNull("unused")))
				}()
				if queryErr = store.Index(queryCtx, &vectorstore.IndexRequest{Documents: docs}); queryErr != nil {
					return nil, queryErr
				}
				response, queryErr := store.Search(queryCtx, &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Filter: predicate, TopK: len(docs)}})
				if queryErr != nil {
					return nil, queryErr
				}
				for _, row := range response.Results {
					ids = append(ids, row.Document.ID)
				}
				return ids, nil
			}})
			for _, facts := range []metadata.Map{nil, {}, {"large": json.RawMessage(`9007199254740993`), "nested": json.RawMessage(`{"decimal":1.0000000000000000001,"huge":1e1000}`)}} {
				if indexErr := store.Index(ctx, &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "one", Text: "text", Metadata: facts}}}); indexErr != nil {
					t.Fatal(indexErr)
				}
				response, searchErr := store.Search(ctx, &vectorstore.SearchRequest{Query: "query"})
				if searchErr != nil {
					t.Fatal(searchErr)
				}
				if len(response.Results) != 1 || !facts.Equal(response.Results[0].Document.Metadata) || (facts == nil) != (response.Results[0].Document.Metadata == nil) {
					t.Fatal("native metadata roundtrip changed its fact")
				}
			}
			selected, selectErr := store.selectItems(ctx, nil)
			if selectErr != nil || len(selected) != 1 {
				t.Fatalf("native selection = %d, %v", len(selected), selectErr)
			}
			if indexErr := store.Index(ctx, &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "one", Text: "changed"}}}); indexErr != nil {
				t.Fatal(indexErr)
			}
			etag := selected[0].etag
			_, deleteErr := container.DeleteItem(ctx, azcosmos.NewPartitionKeyString("library"), "one", &azcosmos.ItemOptions{IfMatchEtag: &etag})
			var nativeErr *azcore.ResponseError
			if !errors.As(deleteErr, &nativeErr) || nativeErr.StatusCode != 412 {
				t.Fatalf("native stale conditional delete = %v", deleteErr)
			}
			if deleteErr = store.DeleteWhere(ctx, filter.IsNull("unused")); deleteErr != nil {
				t.Fatal(deleteErr)
			}
		})
	}
}
