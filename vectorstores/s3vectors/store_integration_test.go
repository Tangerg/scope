//go:build integration

package s3vectors

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3vectors"
	"github.com/aws/aws-sdk-go-v2/service/s3vectors/types"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

func TestNativeS3Vectors(t *testing.T) {
	bucket, region := os.Getenv("SCOPE_S3_VECTORS_BUCKET"), os.Getenv("AWS_REGION")
	if bucket == "" || region == "" {
		t.Fatal("SCOPE_S3_VECTORS_BUCKET and AWS_REGION are required for native integration")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	configuration, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		t.Fatal(err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	configuration.HTTPClient = &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	client := awss3.NewFromConfig(configuration)
	for _, metric := range []types.DistanceMetric{types.DistanceMetricCosine, types.DistanceMetricEuclidean} {
		t.Run(string(metric), func(t *testing.T) {
			var suffix [12]byte
			if _, randomErr := rand.Read(suffix[:]); randomErr != nil {
				t.Fatal(randomErr)
			}
			name := "scope-test-" + hex.EncodeToString(suffix[:])
			if _, createErr := client.CreateIndex(ctx, &awss3.CreateIndexInput{VectorBucketName: aws.String(bucket), IndexName: aws.String(name), DataType: types.DataTypeFloat32, Dimension: aws.Int32(2), DistanceMetric: metric, MetadataConfiguration: &types.MetadataConfiguration{NonFilterableMetadataKeys: []string{contentMetaKey, metadataMetaKey}}}); createErr != nil {
				t.Fatal(createErr)
			}
			t.Cleanup(func() {
				cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
				defer stop()
				if _, deleteErr := client.DeleteIndex(cleanup, &awss3.DeleteIndexInput{VectorBucketName: aws.String(bucket), IndexName: aws.String(name)}); deleteErr != nil {
					t.Error(deleteErr)
				}
			})
			store, storeErr := NewStore(ctx, StoreConfig{Client: client, VectorBucketName: bucket, IndexName: name, EmbeddingModel: constantEmbeddingModel(), DocumentBatcher: testBatcher{size: 32}})
			if storeErr != nil {
				t.Fatal(storeErr)
			}
			storetest.FilterConformance(t, storetest.FilterConfig{Query: func(queryCtx context.Context, docs []*document.Document, predicate filter.Predicate) (ids []string, queryErr error) {
				keys := make([]string, len(docs))
				for i, doc := range docs {
					keys[i] = doc.ID
				}
				defer func() {
					cleanup, stop := context.WithTimeout(context.WithoutCancel(queryCtx), time.Minute)
					defer stop()
					deleteErr := store.DeleteIDs(cleanup, keys)
					queryErr = errors.Join(queryErr, deleteErr)
					if deleteErr == nil {
						queryErr = errors.Join(queryErr, waitForNativeKeys(cleanup, store, 0))
					}
				}()
				if queryErr = store.Index(queryCtx, &vectorstore.IndexRequest{Documents: docs}); queryErr != nil {
					return nil, queryErr
				}
				if queryErr = waitForNativeKeys(queryCtx, store, len(keys)); queryErr != nil {
					return nil, queryErr
				}
				response, queryErr := store.Search(queryCtx, &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: len(docs), Filter: predicate}})
				if queryErr != nil {
					return nil, queryErr
				}
				for _, result := range response.Results {
					ids = append(ids, result.Document.ID)
				}
				return ids, nil
			}})
			for i, source := range []metadata.Map{nil, {}, {"n": json.RawMessage(`9007199254740993`), "nested": json.RawMessage(`{"n":1.0000000000000000001,"huge":1e1000}`)}} {
				doc := &document.Document{ID: "roundtrip", Text: "text", Metadata: source}
				if indexErr := store.Index(ctx, &vectorstore.IndexRequest{Documents: []*document.Document{doc}}); indexErr != nil {
					t.Fatal(indexErr)
				}
				if visibleErr := waitForNativeKeys(ctx, store, 1); visibleErr != nil {
					t.Fatal(visibleErr)
				}
				response, searchErr := store.Search(ctx, &vectorstore.SearchRequest{Query: "query"})
				if searchErr != nil {
					t.Fatal(searchErr)
				}
				if len(response.Results) != 1 || !source.Equal(response.Results[0].Document.Metadata) || (source == nil) != (response.Results[0].Document.Metadata == nil) {
					t.Fatalf("native metadata roundtrip %d changed its fact", i)
				}
				if deleteErr := store.DeleteIDs(ctx, []string{doc.ID}); deleteErr != nil {
					t.Fatal(deleteErr)
				}
				if visibleErr := waitForNativeKeys(ctx, store, 0); visibleErr != nil {
					t.Fatal(visibleErr)
				}
			}
		})
	}
}

func waitForNativeKeys(ctx context.Context, store *Store, count int) error {
	deadline, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		keys, err := store.matchingKeys(deadline, nil)
		if err != nil {
			return err
		}
		if len(keys) == count {
			return nil
		}
		select {
		case <-deadline.Done():
			return deadline.Err()
		case <-ticker.C:
		}
	}
}
