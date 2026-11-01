package mongodb

import (
	"math"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/Tangerg/scope/core/document"

	"github.com/Tangerg/scope/core/vectorstore"
)

func TestIndexPreparesAllVectorsBeforeNativePublication(t *testing.T) {
	for _, coords := range [][]float64{{1}, {math.MaxFloat64, 0}} {
		store, fixture := mongoFilterStore(t, nil)
		b := fixtureBatch([]*document.Document{{ID: "one", Text: "first"}, {ID: "two", Text: "last"}})
		b.Records[1].Values = coords
		if err := store.Upsert(t.Context(), b); err == nil || fixture.writes != 0 {
			t.Fatalf("invalid later coordinates published: %v, %d", err, fixture.writes)
		}
	}
}
func TestNativeScoresMustKeepTheirBoundedScaleEvenBelowMinScore(t *testing.T) {
	for _, score := range []float64{-1, 1.01, math.NaN(), math.Inf(1)} {
		store, collection := mongoFilterStore(t, []*document.Document{{ID: "one", Text: "text"}})
		collection.scores["one"] = score
		response, err := fixtureQuery(t.Context(), store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("q"), Options: vectorstore.SearchOptions{MinScore: 1}})
		if err == nil || response != nil {
			t.Fatalf("native score %v became success: %v %v", score, response, err)
		}
	}
}

func TestNativeEnumerationAndQueryRejectRepeatedIdentity(t *testing.T) {
	store, collection := mongoFilterStore(t, []*document.Document{{ID: "one", Text: "text"}})
	collection.records = append(collection.records, collection.records[0])
	response, err := fixtureQuery(t.Context(), store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("q")})
	if err == nil || response != nil {
		t.Fatal("repeated source became success")
	}
	collection.records = collection.records[:1]
	collection.beforeQuery = func() { collection.records = append(collection.records, collection.records[0]) }
	response, err = fixtureQuery(t.Context(), store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("q"), Options: vectorstore.SearchOptions{TopK: 2}})
	if err == nil || response != nil {
		t.Fatal("repeated native hit became success")
	}
}

func TestDeleteIDsValidatesWholeBatchAndUsesNativeIdentity(t *testing.T) {
	store, collection := mongoFilterStore(t, []*document.Document{{ID: "A", Text: "text"}})
	if err := store.DeleteIDs(t.Context(), []string{"A", " "}); err == nil || len(collection.deleted) != 0 {
		t.Fatal("invalid late identity published deletion")
	}
	if err := store.DeleteIDs(t.Context(), []string{"a"}); err != nil || len(collection.deleted) != 0 {
		t.Fatal("case variant advanced another identity")
	}
	if err := store.DeleteIDs(t.Context(), []string{"A", "A"}); err != nil || len(collection.deleted) != 1 {
		t.Fatal("native identity was not deleted exactly once")
	}
}

func TestSourceRequiresNativeVectorAndNoScoreProjection(t *testing.T) {
	for _, value := range []any{bson.A{1.0, math.Inf(1)}, bson.A{1, 0}, "not a vector"} {
		store, collection := mongoFilterStore(t, []*document.Document{{ID: "one", Text: "text"}})
		raw, err := bson.Marshal(bson.M{idField: "one", contentField: "text", fixtureSpace.StorageKey(): value, metadataField: "null"})
		if err != nil {
			t.Fatal(err)
		}
		collection.records[0] = raw
		response, err := fixtureQuery(t.Context(), store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("q")})
		if err == nil || response != nil {
			t.Fatal("invalid native vector became success")
		}
	}
}
