package pgstore

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestCoreBytesOwnMetadataAndIdentity(t *testing.T) {
	for name, facts := range map[string]metadata.Map{"null": nil, "empty": {}, "exact": {"value": json.RawMessage(`{"exact":9007199254740993,"scale":1e3,"huge":1e1000000,"nul":"\u0000"}`)}} {
		t.Run(name, func(t *testing.T) {
			encoded, err := facts.MarshalJSON()
			if err != nil {
				t.Fatal(err)
			}
			doc, err := decodeDocument([]byte("native\x00文"), "text", encoded)
			if err != nil || doc.ID != "native\x00文" || (doc.Metadata == nil) != (facts == nil) || !doc.Metadata.Equal(facts) {
				t.Fatalf("native bytes: document=%v error=%v", doc, err)
			}
		})
	}
	for _, facts := range [][]byte{nil, {}, []byte("[]"), []byte("broken")} {
		if _, err := decodeDocument([]byte("one"), "text", facts); err == nil {
			t.Fatalf("malformed Core bytes %q accepted", facts)
		}
	}
	if _, err := decodeDocument(nil, "text", []byte("null")); err == nil {
		t.Fatal("missing native ID accepted")
	}
	if _, err := decodeDocument([]byte("one"), "", []byte("null")); err == nil {
		t.Fatal("missing content accepted")
	}
}

func TestNativeVectorAndScoreContracts(t *testing.T) {
	for _, metric := range []DistanceMetric{DistanceCosine, DistanceL2, DistanceIP} {
		for _, vector := range [][]float64{{1}, {math.MaxFloat64, 0}, {math.SmallestNonzeroFloat64, 0}, {math.NaN(), 0}, {math.Inf(1), 0}} {
			if _, err := metric.vector(vector, 2); err == nil {
				t.Fatalf("%s accepted %v", metric, vector)
			}
		}
		for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
			if _, err := metric.score(value); err == nil {
				t.Fatalf("%s accepted distance %v", metric, value)
			}
		}
	}
	if _, err := DistanceCosine.vector([]float64{0, 0}, 2); err == nil {
		t.Fatal("zero cosine vector accepted")
	}
	if _, err := DistanceL2.vector([]float64{0, 0}, 2); err != nil {
		t.Fatal(err)
	}
	for _, distance := range []float64{-1, 3} {
		if _, err := DistanceCosine.score(distance); err == nil {
			t.Fatal("invalid cosine distance accepted")
		}
	}
	if _, err := DistanceL2.score(-1); err == nil {
		t.Fatal("negative distance accepted")
	}
}

func TestCoreSelectsAllRowsBeforeEffects(t *testing.T) {
	facts, err := metadata.FromValues(map[string]any{"value": 1})
	if err != nil {
		t.Fatal(err)
	}
	predicate, err := filter.Parse("value[2147483648] is null")
	if err != nil {
		t.Fatal(err)
	}
	docs := []*document.Document{{ID: "one", Text: "text", Metadata: facts}}
	ids, err := selectIDs(docs, predicate)
	if err != nil || len(ids) != 1 || string(ids[0]) != "one" {
		t.Fatalf("native int4 restriction leaked into Core: ids=%v error=%v", ids, err)
	}
	if err = docs[0].Metadata.Set("value", "wrong type"); err != nil {
		t.Fatal(err)
	}
	predicate, err = filter.Parse("value < 2")
	if err != nil {
		t.Fatal(err)
	}
	if ids, err := selectIDs(docs, predicate); err == nil || ids != nil {
		t.Fatalf("type error concealed: ids=%v error=%v", ids, err)
	}
}
