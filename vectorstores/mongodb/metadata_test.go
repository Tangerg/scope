package mongodb

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
)

type metadataCollection struct {
	DocumentCollection
	document bson.M
}

func (m *metadataCollection) BulkWrite(_ context.Context, models []mongo.WriteModel, _ ...options.Lister[options.BulkWriteOptions]) (*mongo.BulkWriteResult, error) {
	if len(models) != 1 {
		return nil, fmt.Errorf("expected one replacement, got %d", len(models))
	}
	replacement, ok := models[0].(*mongo.ReplaceOneModel)
	if !ok {
		return nil, fmt.Errorf("expected replacement, got %T", models[0])
	}
	encoded, err := bson.Marshal(replacement.Replacement)
	if err != nil {
		return nil, err
	}
	if err := bson.Unmarshal(encoded, &m.document); err != nil {
		return nil, err
	}
	return &mongo.BulkWriteResult{Acknowledged: true, UpsertedCount: 1}, nil
}

func TestIndexMetadataNumbersRoundTripThroughBSON(t *testing.T) {
	t.Parallel()
	values := metadata.Map{
		"integer":  json.RawMessage(`9007199254740993.0`),
		"exponent": json.RawMessage(`9.007199254740993e15`),
		"fraction": json.RawMessage(`0.1`),
		"small":    json.RawMessage(`1e-16`),
		"nested":   json.RawMessage(`{"number":9007199254740993.0}`),
		"list":     json.RawMessage(`[9007199254740993.0,0.1]`),
	}
	collection := &metadataCollection{}
	store := upsertStore(t, collection)
	err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "one", Text: "content", Metadata: values}}})
	if err != nil {
		t.Fatal(err)
	}
	collection.document[scoreField] = 0.75
	result, err := store.toMatch(collection.document)
	if err != nil {
		t.Fatal(err)
	}
	expected := metadata.Map{
		"integer":  json.RawMessage(`9007199254740993`),
		"exponent": json.RawMessage(`9007199254740993`),
		"fraction": json.RawMessage(`0.1`),
		"small":    json.RawMessage(`1e-16`),
		"nested":   json.RawMessage(`{"number":9007199254740993}`),
		"list":     json.RawMessage(`[9007199254740993,0.1]`),
	}
	if !result.Document.Metadata.Equal(expected) {
		t.Fatalf("round-trip metadata = %s, want %s", result.Document.Metadata, expected)
	}
}

func TestIndexRejectsMetadataNumberLossBeforeBulkWrite(t *testing.T) {
	t.Parallel()
	for _, value := range []string{`1.00000000000000001`, `9223372036854775807.1`, `1e1000`, `{"items":[1.00000000000000001]}`} {
		t.Run(value, func(t *testing.T) {
			collection := &countingCollection{result: &mongo.BulkWriteResult{Acknowledged: true, UpsertedCount: 1}}
			err := upsertStore(t, collection).Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "one", Text: "content", Metadata: metadata.Map{"value": json.RawMessage(value)}}}})
			if err == nil {
				t.Fatal("Index accepted metadata that cannot be represented without loss")
			}
			if collection.sent != 0 {
				t.Fatalf("sent %d writes before rejecting metadata", collection.sent)
			}
		})
	}
}
