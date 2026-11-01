package mongodb

import (
	"errors"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/Tangerg/scope/core/document"
)

func TestConstructionReadsNativePolicyAndRejectsInvalidIndexes(t *testing.T) {
	for _, metric := range []string{"cosine", "euclidean", "dotProduct"} {
		collection := &filterCollection{policy: []any{nativeIndexPolicy(DefaultVectorIndexName, metric, 3)}}
		store, err := NewStore(t.Context(), StoreConfig{Collection: collection, Space: fixtureSpace})
		if err != nil {
			t.Fatal(err)
		}
		err = store.Upsert(t.Context(), fixtureBatch([]*document.Document{{ID: "one", Text: "text"}}))
		if err == nil || collection.writes != 0 {
			t.Fatal("native dimension did not constrain index publication")
		}
	}
	for name, mutate := range map[string]func(bson.M){
		"wrong name":             func(index bson.M) { index["name"] = "different" },
		"wrong type":             func(index bson.M) { index["type"] = "search" },
		"building but queryable": func(index bson.M) { index["status"] = "BUILDING" },
		"not queryable":          func(index bson.M) { index["queryable"] = false },
		"missing definition":     func(index bson.M) { delete(index, "latestDefinition") },
		"wrong path": func(index bson.M) {
			index["latestDefinition"].(bson.M)["fields"].(bson.A)[0].(bson.M)["path"] = "other"
		},
		"missing dimension": func(index bson.M) {
			index["latestDefinition"].(bson.M)["fields"].(bson.A)[0].(bson.M)["numDimensions"] = 0
		},
		"unknown metric": func(index bson.M) {
			index["latestDefinition"].(bson.M)["fields"].(bson.A)[0].(bson.M)["similarity"] = "unknown"
		},
		"missing identity filter": func(index bson.M) {
			index["latestDefinition"].(bson.M)["fields"] = index["latestDefinition"].(bson.M)["fields"].(bson.A)[:1]
		},
	} {
		t.Run(name, func(t *testing.T) {
			index := nativeIndexPolicy(DefaultVectorIndexName, "cosine", 2)
			mutate(index)
			collection := &filterCollection{policy: []any{index}}
			store, err := NewStore(t.Context(), StoreConfig{Collection: collection, Space: fixtureSpace})
			if err == nil || store != nil {
				t.Fatal("invalid native policy produced a store")
			}
		})
	}
	for _, policy := range [][]any{nil, {nativeIndexPolicy(DefaultVectorIndexName, "cosine", 2), nativeIndexPolicy(DefaultVectorIndexName, "cosine", 2)}} {
		store, err := NewStore(t.Context(), StoreConfig{Collection: &filterCollection{policy: policy}, Space: fixtureSpace})
		if err == nil || store != nil {
			t.Fatal("missing or ambiguous native policy produced a store")
		}
	}
}

func TestConstructionRejectsTypedNilWithoutReadingCollection(t *testing.T) {
	config := StoreConfig{Collection: &filterCollection{policy: []any{nativeIndexPolicy(DefaultVectorIndexName, "cosine", 2)}}, Space: fixtureSpace}
	for _, mutate := range []func(*StoreConfig){
		func(candidate *StoreConfig) { candidate.Collection = (*mongo.Collection)(nil) },
		func(c *StoreConfig) { c.Space = "" },
		func(c *StoreConfig) { c.Space = " padded" },
		func(candidate *StoreConfig) { candidate.NumCandidates = -1 },
		func(candidate *StoreConfig) { candidate.VectorIndexName = " index " },
	} {
		candidate := config
		mutate(&candidate)
		if store, err := NewStore(t.Context(), candidate); err == nil || store != nil {
			t.Fatal("invalid configuration reached native IO")
		}
	}
	collection := config.Collection.(*filterCollection)
	collection.records = []bson.Raw{nativeRecord(t, "one", `{"key":1,"key":2}`)}
	if store, err := NewStore(t.Context(), config); err != nil || store == nil {
		t.Fatalf("construction read stored documents: %v", err)
	}
	collection.records = nil
	collection.enumerationErr = errors.New("native enumeration denied")
	if store, err := NewStore(t.Context(), config); err != nil || store == nil {
		t.Fatalf("construction required source read permission: %v", err)
	}
}
