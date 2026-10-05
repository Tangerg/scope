//go:build integration

package qdrant

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	qdrantclient "github.com/qdrant/go-client/qdrant"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

type integrationBatcher struct{}

func (i integrationBatcher) Batch(_ context.Context, docs []*document.Document) ([][]*document.Document, error) {
	return [][]*document.Document{docs}, nil
}

func liveStore(t *testing.T, metric qdrantclient.Distance) (*Store, *qdrantclient.Client, string) {
	t.Helper()
	address := os.Getenv("SCOPE_QDRANT_ADDR")
	if address == "" {
		t.Fatal("SCOPE_QDRANT_ADDR is required with -tags=integration")
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	client, err := qdrantclient.NewClient(&qdrantclient.Config{Host: host, Port: port, APIKey: os.Getenv("SCOPE_QDRANT_API_KEY"), UseTLS: os.Getenv("SCOPE_QDRANT_TLS") == "true"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := client.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	name := "scope_native_" + rand.Text()
	if err = client.CreateCollection(t.Context(), &qdrantclient.CreateCollection{CollectionName: name, VectorsConfig: qdrantclient.NewVectorsConfig(&qdrantclient.VectorParams{Size: 2, Distance: metric})}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
		defer cancel()
		if deleteErr := client.DeleteCollection(ctx, name); deleteErr != nil {
			t.Error(deleteErr)
		}
	})
	model := embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
		outputs := make([]*embedding.Output, len(request.Texts))
		for i := range outputs {
			outputs[i] = &embedding.Output{Embedding: []float64{1, 0}}
		}
		return embedding.NewResponse(outputs, nil)
	})
	store, err := NewStore(t.Context(), StoreConfig{Client: client, CollectionName: name, EmbeddingModel: model, DocumentBatcher: integrationBatcher{}})
	if err != nil {
		t.Fatal(err)
	}
	return store, client, name
}

func TestLiveFilterConformance(t *testing.T) {
	for _, metric := range []qdrantclient.Distance{qdrantclient.Distance_Cosine, qdrantclient.Distance_Dot, qdrantclient.Distance_Euclid, qdrantclient.Distance_Manhattan} {
		t.Run(metric.String(), func(t *testing.T) {
			store, _, _ := liveStore(t, metric)
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
				request := &vectorstore.SearchRequest{Query: "filter conformance", Options: vectorstore.SearchOptions{TopK: len(docs), Filter: predicate}}
				response, err := store.Search(ctx, request)
				if err != nil {
					return nil, err
				}
				var ids []string
				for _, hit := range response.Results {
					ids = append(ids, hit.Document.ID)
				}
				if err = store.DeleteWhere(ctx, predicate); err != nil {
					return nil, err
				}
				remaining, err := store.Search(ctx, request)
				if err != nil || len(remaining.Results) != 0 {
					return nil, errors.Join(err, errors.New("native conditional delete retained matches"))
				}
				return ids, nil
			}})
		})
	}
}

func TestLiveExactMetadataAndRawNativeScores(t *testing.T) {
	for _, metric := range []qdrantclient.Distance{qdrantclient.Distance_Cosine, qdrantclient.Distance_Dot, qdrantclient.Distance_Euclid, qdrantclient.Distance_Manhattan} {
		t.Run(metric.String(), func(t *testing.T) {
			store, client, name := liveStore(t, metric)
			docs := []*document.Document{{ID: "0", Text: "nil🙂"}, {ID: "18446744073709551615", Text: "empty", Metadata: metadata.Map{}}, {ID: "f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4", Text: "exact", Metadata: metadata.Map{"huge": json.RawMessage(`1e1000`), "integer": json.RawMessage(`9007199254740993`), "nested": json.RawMessage(`{"items":[null,true,{}]}`)}}}
			if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs}); err != nil {
				t.Fatal(err)
			}
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q", Options: vectorstore.SearchOptions{TopK: 3}})
			if err != nil || len(response.Results) != 3 {
				t.Fatalf("response=%v err=%v", response, err)
			}
			for _, hit := range response.Results {
				found := false
				for _, doc := range docs {
					if doc.ID == hit.Document.ID {
						found = doc.Text == hit.Document.Text && doc.Metadata.Equal(hit.Document.Metadata) && (doc.Metadata == nil) == (hit.Document.Metadata == nil)
					}
				}
				if !found {
					t.Fatalf("native metadata changed: %#v", hit.Document)
				}
			}
			hits, err := client.Query(t.Context(), &qdrantclient.QueryPoints{CollectionName: name, Query: qdrantclient.NewQueryDense([]float32{1, 0}), Limit: new(uint64(3)), WithPayload: qdrantclient.NewWithPayload(true), WithVectors: qdrantclient.NewWithVectors(true)})
			if err != nil || len(hits) != 3 {
				t.Fatalf("raw query=%v error=%v", hits, err)
			}
			for _, hit := range hits {
				id, identityErr := formatPointID(hit.Id)
				if identityErr != nil {
					t.Fatal(identityErr)
				}
				score, scoreErr := store.schema.score(float64(hit.Score))
				if scoreErr != nil {
					t.Fatal(scoreErr)
				}
				found := false
				for _, result := range response.Results {
					if result.Document.ID == id && result.Score == score {
						found = true
					}
				}
				if !found {
					t.Fatalf("native score changed: %v", hit)
				}
			}
		})
	}
}

type changedNativeClient struct {
	APIClient
	native *qdrantclient.Client
	name   string
}

func (c *changedNativeClient) Delete(ctx context.Context, request *qdrantclient.DeletePoints) (*qdrantclient.UpdateResult, error) {
	changed, err := c.native.SetPayload(ctx, &qdrantclient.SetPayloadPoints{CollectionName: c.name, Wait: new(true), PointsSelector: qdrantclient.NewPointsSelector(qdrantclient.NewIDNum(1)), Payload: map[string]*qdrantclient.Value{metadataField: {Kind: &qdrantclient.Value_StringValue{StringValue: `{"value":"b"}`}}}})
	if err != nil {
		return nil, err
	}
	if err = requireAppliedUpdate(changed, "native concurrent payload update"); err != nil {
		return nil, err
	}
	return c.native.Delete(ctx, request)
}

func TestLiveConditionalDeletionRetainsMetadataUpdateAndRejectsCorruption(t *testing.T) {
	store, client, name := liveStore(t, qdrantclient.Distance_Cosine)
	facts, err := metadata.FromValues(map[string]any{"value": "a"})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "1", Text: "changed", Metadata: facts}, {ID: "2", Text: "unchanged", Metadata: facts}}}); err != nil {
		t.Fatal(err)
	}
	store.client = &changedNativeClient{APIClient: client, native: client, name: name}
	if err = store.DeleteWhere(t.Context(), filter.EQ("value", "a")); err != nil {
		t.Fatal(err)
	}
	hits, err := client.Get(t.Context(), &qdrantclient.GetPoints{CollectionName: name, Ids: []*qdrantclient.PointId{qdrantclient.NewIDNum(1), qdrantclient.NewIDNum(2)}, WithPayload: qdrantclient.NewWithPayload(true)})
	if err != nil || len(hits) != 1 || hits[0].Id.GetNum() != 1 || hits[0].Payload[metadataField].GetStringValue() != `{"value":"b"}` {
		t.Fatalf("native deletion lost changed fact: %v, %v", hits, err)
	}
	updated, err := client.SetPayload(t.Context(), &qdrantclient.SetPayloadPoints{CollectionName: name, Wait: new(true), PointsSelector: qdrantclient.NewPointsSelector(qdrantclient.NewIDNum(1)), Payload: map[string]*qdrantclient.Value{"legacy": {Kind: &qdrantclient.Value_StringValue{StringValue: "extra"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = requireAppliedUpdate(updated, "native corruption fixture"); err != nil {
		t.Fatal(err)
	}
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q", Options: vectorstore.SearchOptions{Filter: filter.EQ("value", "absent"), MinScore: 1}})
	if err == nil || response != nil {
		t.Fatalf("native corrupt source became success: %v, %v", response, err)
	}
	if invalid, constructorErr := NewStore(t.Context(), StoreConfig{Client: client, CollectionName: name, EmbeddingModel: embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) {
		return nil, errors.New("unexpected model call")
	}), DocumentBatcher: integrationBatcher{}}); constructorErr == nil || invalid != nil {
		t.Fatal("constructor accepted an incompatible namespace")
	}
}

func TestLiveRawRankingAcrossMetadataGroups(t *testing.T) {
	store, client, name := liveStore(t, qdrantclient.Distance_Dot)
	var docs []*document.Document
	var points []*qdrantclient.PointStruct
	for i := range filterPageSize + 1 {
		facts, err := metadata.FromValues(map[string]any{"value": "a", "unique": i})
		if err != nil {
			t.Fatal(err)
		}
		doc := &document.Document{ID: fmt.Sprint(i + 1), Text: "text", Metadata: facts}
		docs = append(docs, doc)
		point, err := encodeRecord(doc)
		if err != nil {
			t.Fatal(err)
		}
		point.Vectors = qdrantclient.NewVectorsDense([]float32{float32(i + 40), 0})
		points = append(points, point)
	}
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs}); err != nil {
		t.Fatal(err)
	}
	applied, err := client.Upsert(t.Context(), &qdrantclient.UpsertPoints{CollectionName: name, Wait: new(true), Points: points})
	if err != nil {
		t.Fatal(err)
	}
	if err = requireAppliedUpdate(applied, "native rank fixture"); err != nil {
		t.Fatal(err)
	}
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q", Options: vectorstore.SearchOptions{TopK: 1, Filter: filter.EQ("value", "a")}})
	if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != docs[len(docs)-1].ID {
		t.Fatalf("native rank was normalized before TopK: %v, %v", response, err)
	}
}
