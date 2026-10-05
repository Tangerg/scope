package mongodb

import (
	"encoding/json"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
)

func TestCoreMetadataIsOneJSONStringThroughNativeBSON(t *testing.T) {
	for _, facts := range []metadata.Map{nil, {}, {
		"huge": json.RawMessage(`1e1000`), "exact": json.RawMessage(`1.00000000000000001`), "integer": json.RawMessage(`9007199254740993`),
		"$native.key": json.RawMessage(`{"array":[null,true,{"number":9223372036854775807.1}]}`),
		"content":     json.RawMessage(`"business text"`),
	}} {
		doc := &document.Document{ID: "one", Text: "stored text", Metadata: facts}
		store, collection := mongoFilterStore(t, []*document.Document{doc})
		raw := collection.records[0]
		expected, err := facts.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		if raw.Lookup(metadataField).StringValue() != string(expected) {
			t.Fatalf("native JSON=%v", raw)
		}
		response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q"})
		if err != nil || len(response.Results) != 1 {
			t.Fatalf("response=%v err=%v", response, err)
		}
		got := response.Results[0].Document
		if got.ID != doc.ID || got.Text != doc.Text || !got.Metadata.Equal(facts) || (got.Metadata == nil) != (facts == nil) {
			t.Fatalf("round trip=%v", got)
		}
	}
}

func TestCurrentSchemaRejectsLegacyAndMalformedRecordsOutsideFilter(t *testing.T) {
	for _, bad := range []bson.D{
		{{Key: idField, Value: "bad"}, {Key: contentField, Value: "text"}, {Key: "metadata", Value: bson.M{"value": "outside"}}, {Key: embeddingField, Value: []float32{1, 0}}},
		{{Key: idField, Value: "bad"}, {Key: contentField, Value: "text"}, {Key: metadataField, Value: `{"key":1,"key":2}`}, {Key: embeddingField, Value: []float32{1, 0}}},
		{{Key: idField, Value: "bad"}, {Key: contentField, Value: "text"}, {Key: metadataField, Value: `null`}, {Key: embeddingField, Value: []float32{1}}},
		{{Key: idField, Value: "bad"}, {Key: idField, Value: "duplicate"}, {Key: contentField, Value: "text"}, {Key: metadataField, Value: `null`}, {Key: embeddingField, Value: []float32{1, 0}}},
	} {
		store, collection := mongoFilterStore(t, []*document.Document{{ID: "one", Text: "text"}})
		raw, err := bson.Marshal(bad)
		if err != nil {
			t.Fatal(err)
		}
		collection.records = append(collection.records, raw)
		response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q", Options: vectorstore.SearchOptions{MinScore: 1}})
		if err == nil || response != nil {
			t.Fatalf("invalid source became success: %v %v", response, err)
		}
	}
}
