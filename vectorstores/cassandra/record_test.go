package cassandra

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"testing"

	gocql "github.com/apache/cassandra-gocql-driver/v2"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
)

func TestStoredRecordPreservesCoreFacts(t *testing.T) {
	for _, facts := range []metadata.Map{nil, {}, {"value": json.RawMessage(`9007199254740993.0`), "nested": json.RawMessage(`{"values":[null,1e1000]}`), "exact": json.RawMessage(`1.00000000000000001`)}} {
		record, err := encodeStoredRecord(&document.Document{ID: "id/路径", Text: "content", Metadata: facts}, []float64{1, 0})
		if err != nil {
			t.Fatal(err)
		}
		doc, err := decodeStoredRecord(record)
		if err != nil {
			t.Fatal(err)
		}
		if doc.ID != "id/路径" || doc.Text != "content" || !facts.Equal(doc.Metadata) || (facts == nil) != (doc.Metadata == nil) {
			t.Fatalf("document=%+v metadata=%s want %s", doc, doc.Metadata, facts)
		}
	}
}
func TestStoredRecordRejectsMalformedData(t *testing.T) {
	cases := []storedRecord{{id: "id", content: "content", metadata: "", vector: []float32{1, 0}}, {id: "id", content: "content", metadata: `{"a":1,"a":2}`, vector: []float32{1, 0}}, {id: "id", content: "content", metadata: `[]`, vector: []float32{1, 0}}, {id: " ", content: "content", metadata: `null`, vector: []float32{1, 0}}, {id: "id", content: "", metadata: `null`, vector: []float32{1, 0}}, {id: "id", content: "content", metadata: `null`, vector: []float32{}}, {id: "id", content: "content", metadata: `null`, vector: []float32{float32(math.Inf(1))}}}
	for i, record := range cases {
		if _, err := decodeStoredRecord(record); err == nil {
			t.Fatalf("accepted malformed record %d", i)
		}
	}
	for _, vector := range [][]float64{nil, {math.MaxFloat64, 1}} {
		if _, err := encodeStoredRecord(&document.Document{ID: "id", Text: "content"}, vector); err == nil {
			t.Fatal("accepted invalid native vector", vector)
		}
	}
	if _, err := encodeStoredRecord(&document.Document{ID: "id", Text: "content", Metadata: metadata.Map{"a": json.RawMessage(`invalid`)}}, []float64{1, 0}); err == nil {
		t.Fatal("accepted invalid metadata")
	}
}
func TestSimilarityProjectsNativeScores(t *testing.T) {
	tests := []struct {
		metric SimilarityFunction
		raw    float64
		want   vectorstore.Score
	}{{SimilarityCosine, .5, .5}, {SimilarityEuclidean, 1, 1}, {SimilarityDotProduct, .5, .5}, {SimilarityDotProduct, 100.5, 1}, {SimilarityDotProduct, -1000, 0}}
	for _, test := range tests {
		score, err := test.metric.score(test.raw)
		if err != nil || score != test.want {
			t.Fatalf("score=%v err=%v want %v", score, err, test.want)
		}
	}
	for _, metric := range []SimilarityFunction{SimilarityCosine, SimilarityEuclidean, SimilarityDotProduct} {
		if !metric.Valid() || metric.String() == "" {
			t.Fatal(metric)
		}
		for _, raw := range []float64{math.NaN(), math.Inf(1)} {
			if _, err := metric.score(raw); !errors.Is(err, vectorstore.ErrInvalidScore) {
				t.Fatal(err)
			}
		}
	}
	if _, err := SimilarityCosine.score(2); err == nil {
		t.Fatal("clamped invalid cosine score")
	}
	if SimilarityFunction("wrong").Valid() {
		t.Fatal("accepted wrong metric")
	}
}

type nilSession struct{ Session }

type unitBatcher struct{}

func (unitBatcher) Batch(_ context.Context, documents []*document.Document) ([][]*document.Document, error) {
	return [][]*document.Document{documents}, nil
}

func TestConfigValidatesCurrentSchemaInputs(t *testing.T) {
	valid := StoreConfig{Session: &gocql.Session{}, EmbeddingModel: embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) { return nil, nil }), DocumentBatcher: unitBatcher{}}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	valid.applyDefaults()
	for _, field := range []string{"IDColumn", "ContentColumn", "MetadataColumn", "EmbeddingColumn", "KeyspaceName", "TableName"} {
		config := valid
		reflect.ValueOf(&config).Elem().FieldByName(field).SetString("invalid-name")
		if err := config.Validate(); err == nil {
			t.Fatal("accepted invalid", field)
		}
	}
	for _, field := range []string{"ContentColumn", "MetadataColumn", "EmbeddingColumn"} {
		config := valid
		reflect.ValueOf(&config).Elem().FieldByName(field).SetString(valid.IDColumn)
		if err := config.Validate(); err == nil {
			t.Fatal("accepted duplicate", field)
		}
	}
	var typedNil *nilSession
	invalid := []StoreConfig{valid, valid, valid, valid, valid, valid, valid}
	invalid[0].Session = typedNil
	invalid[1].EmbeddingModel = nil
	invalid[2].DocumentBatcher = nil
	invalid[3].Similarity = "wrong"
	invalid[4].InitializeSchema = true
	invalid[5].CreateDimensions = 2
	invalid[6].InitializeSchema = true
	invalid[6].CreateDimensions = -1
	for _, config := range invalid {
		if err := config.Validate(); err == nil {
			t.Fatal("accepted invalid config")
		}
		if store, err := NewStore(t.Context(), config); store != nil || err == nil {
			t.Fatalf("constructor=%v err=%v", store, err)
		}
	}
	if got := quoteIdentifier(`a"b`); got != `"a""b"` {
		t.Fatal(got)
	}
}
