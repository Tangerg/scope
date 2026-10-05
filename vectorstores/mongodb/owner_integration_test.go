//go:build integration

package mongodb

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestLiveExactMetadataAndNativeScores(t *testing.T) {
	for _, metric := range []string{"cosine", "euclidean", "dotProduct"} {
		t.Run(metric, func(t *testing.T) {
			store, collection := liveMongoStore(t, metric)
			docs := []*document.Document{{ID: "nil", Text: "text"}, {ID: "empty", Text: "text", Metadata: metadata.Map{}}, {ID: "exact", Text: "text🙂", Metadata: metadata.Map{"huge": json.RawMessage(`1e1000`), "long": json.RawMessage(`1.00000000000000001`), "$business.key": json.RawMessage(`{"array":[null,{},9007199254740993]}`)}}}
			if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs}); err != nil {
				t.Fatal(err)
			}
			if err := awaitVisibleDocuments(t.Context(), store, docs); err != nil {
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
						found = hit.Document.Text == doc.Text && doc.Metadata.Equal(hit.Document.Metadata) && (doc.Metadata == nil) == (hit.Document.Metadata == nil)
					}
				}
				if !found {
					t.Fatalf("native metadata changed: %v", hit.Document)
				}
			}
			cursor, err := collection.Aggregate(t.Context(), mongo.Pipeline{{{Key: "$vectorSearch", Value: bson.M{"index": DefaultVectorIndexName, "path": embeddingField, "queryVector": []float32{1, 0}, "numCandidates": 200, "limit": 3}}}, {{Key: "$addFields", Value: bson.M{scoreField: bson.M{"$meta": "vectorSearchScore"}}}}})
			if err != nil {
				t.Fatal(err)
			}
			var native []bson.M
			if err = cursor.All(t.Context(), &native); err != nil {
				t.Fatal(err)
			}
			if len(native) != 3 {
				t.Fatalf("native query=%v", native)
			}
			for _, row := range native {
				found := false
				for _, hit := range response.Results {
					if row[idField] == hit.Document.ID && row[scoreField] == hit.Score.Float64() {
						found = true
					}
				}
				if !found {
					t.Fatalf("native score changed: %v", row)
				}
			}
		})
	}
}

type changedMongoCollection struct{ *mongo.Collection }

func (c *changedMongoCollection) DeleteMany(ctx context.Context, constraint any, builders ...options.Lister[options.DeleteManyOptions]) (*mongo.DeleteResult, error) {
	result, err := c.UpdateOne(ctx, bson.M{idField: "changed"}, bson.M{"$set": bson.M{metadataField: `{"value":"b"}`}})
	if err != nil {
		return nil, err
	}
	if !result.Acknowledged || result.MatchedCount != 1 {
		return nil, vectorstore.ErrInvalidDocument
	}
	return c.Collection.DeleteMany(ctx, constraint, builders...)
}

func TestLiveConditionalDeletionAndBinaryIdentity(t *testing.T) {
	store, collection := liveMongoStore(t, "cosine")
	docs := []*document.Document{{ID: "changed", Text: "text", Metadata: metadata.Map{"value": json.RawMessage(`"a"`)}}, {ID: "unchanged", Text: "text", Metadata: metadata.Map{"value": json.RawMessage(`"a"`)}}, {ID: "A", Text: "text"}}
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteIDs(t.Context(), []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if count, err := collection.CountDocuments(t.Context(), bson.M{idField: "A"}); err != nil || count != 1 {
		t.Fatalf("case variant lost identity: %d %v", count, err)
	}
	store.collection = &changedMongoCollection{Collection: collection}
	if err := store.DeleteWhere(t.Context(), filter.EQ("value", "a")); err != nil {
		t.Fatal(err)
	}
	cursor, err := collection.Find(t.Context(), bson.M{})
	if err != nil {
		t.Fatal(err)
	}
	var rows []bson.M
	if err = cursor.All(t.Context(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("native deletion lost changed facts: %v", rows)
	}
	for _, row := range rows {
		if row[idField] == "unchanged" {
			t.Fatal("native conditional deletion retained unchanged record")
		}
	}
	if _, err = collection.UpdateOne(t.Context(), bson.M{idField: "changed"}, bson.M{"$set": bson.M{"legacy": true}}); err != nil {
		t.Fatal(err)
	}
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q", Options: vectorstore.SearchOptions{Filter: filter.EQ("value", "outside"), MinScore: 1}})
	if err == nil || response != nil {
		t.Fatalf("corrupt native source became success: %v %v", response, err)
	}
}

type groupedMongoCollection struct {
	*mongo.Collection
	groups []int
}

func (g *groupedMongoCollection) Aggregate(ctx context.Context, pipeline any, builders ...options.Lister[options.AggregateOptions]) (*mongo.Cursor, error) {
	stages := pipeline.(mongo.Pipeline)
	if len(stages) != 0 && stages[0][0].Key == "$vectorSearch" {
		native := stages[0][0].Value.(bson.M)
		if constraint, ok := native["filter"].(bson.M); ok {
			g.groups = append(g.groups, len(constraint[idField].(bson.M)["$in"].([]string)))
		}
	}
	return g.Collection.Aggregate(ctx, pipeline, builders...)
}

func TestLiveNativeRankingAcrossIdentityGroups(t *testing.T) {
	store, collection := liveMongoStore(t, "cosine")
	model, err := embeddingclient.New(embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
		outputs := make([]*embedding.Output, len(request.Texts))
		for i, text := range request.Texts {
			vector := []float64{0, 1}
			if text == "best" || text == "q" {
				vector = []float64{1, 0}
			}
			outputs[i] = &embedding.Output{Embedding: vector}
		}
		return embedding.NewResponse(outputs, nil)
	}))
	if err != nil {
		t.Fatal(err)
	}
	store.embeddingClient = model
	var docs []*document.Document
	for i := range filterPageSize + 1 {
		text := "other"
		if i == filterPageSize {
			text = "best"
		}
		docs = append(docs, &document.Document{ID: fmt.Sprintf("%03d", i), Text: text, Metadata: metadata.Map{"value": json.RawMessage(`"x"`)}})
	}
	if err = store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs}); err != nil {
		t.Fatal(err)
	}
	if err = awaitVisibleDocuments(t.Context(), store, docs); err != nil {
		t.Fatal(err)
	}
	native := &groupedMongoCollection{Collection: collection}
	store.collection = native
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q", Options: vectorstore.SearchOptions{TopK: 1, Filter: filter.EQ("value", "x")}})
	if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != docs[filterPageSize].ID || response.Results[0].Score != 1 || !slices.Equal(native.groups, []int{filterPageSize, 1}) {
		t.Fatalf("groups=%v response=%v error=%v", native.groups, response, err)
	}
}
