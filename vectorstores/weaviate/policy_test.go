package weaviate

import (
	"errors"
	"testing"

	"encoding/json"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
)

func TestNativeCollectionOwnsMetricWithoutConfiguredCounterpart(t *testing.T) {
	for _, metric := range []string{distanceCosine, distanceDot, distanceL2Squared, distanceHamming, distanceManhattan} {
		store := newNativeStore(t, &nativeFixture{class: nativeClass("Documents", metric)})
		if store.schema.metric != metric {
			t.Fatalf("metric=%s", store.schema.metric)
		}
	}
}

func TestNativeSchemaRejectsCompetingVectorizersAndInvalidRepresentations(t *testing.T) {
	for _, name := range []string{"valid disabled multivector", "valid flat", "wrong target", "extra target", "collection alias", "vectorizer", "tenancy", "distance", "flat missing distance", "skipped", "multivector", "bad content", "bad terms", "bad metadata", "extra property", "repeated property", "inconsistent dynamic"} {
		t.Run(name, func(t *testing.T) {
			v := vectorWire{Vectorizer: map[string]json.RawMessage{"none": json.RawMessage("{}")}, IndexType: "hnsw"}
			v.Index.Distance = distanceCosine
			c := collectionWire{Name: "Documents", Description: fixtureSpace.StorageKey(), Vectors: map[string]vectorWire{}, Properties: []propertyWire{{Name: fieldContent, DataType: []string{"text"}, Tokenization: "word"}, {Name: fieldTerms, DataType: []string{"text[]"}, Tokenization: "field"}, {Name: fieldMetadata, DataType: []string{"text"}, Tokenization: "field"}}}
			key := vectorName
			switch name {
			case "valid flat":
				v.IndexType = "flat"
			case "wrong target":
				key = "other"
			case "extra target":
				c.Vectors["other"] = v
			case "collection alias":
				c.Name = "Other"
			case "vectorizer":
				v.Vectorizer = nil
			case "tenancy":
				c.MultiTenancy.Enabled = true
			case "distance":
				v.Index.Distance = "unknown"
			case "flat missing distance":
				v.IndexType = "flat"
				v.Index.Distance = ""
			case "skipped":
				v.Index.Skip = true
			case "multivector":
				v.Index.Multi.Enabled = true
			case "bad content":
				c.Properties[0].IndexSearchable = new(false)
			case "bad terms":
				c.Properties[1].Tokenization = "word"
			case "bad metadata":
				c.Properties[2].IndexFilterable = new(false)
			case "extra property":
				c.Properties = append(c.Properties, propertyWire{Name: "another"})
			case "repeated property":
				c.Properties[1] = c.Properties[0]
			case "inconsistent dynamic":
				v.IndexType = "dynamic"
				v.Index.HNSW.Distance = distanceDot
			}
			c.Vectors[key] = v
			var schema nativeSchema
			err := schema.read(&c, "Documents", fixtureSpace)
			if name == "valid disabled multivector" || name == "valid flat" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, ErrIncompatibleCollection) {
				t.Fatalf("invalid schema admitted: %v", err)
			}
		})
	}
}

func TestWrongSpaceReprovisioningPreventsWrite(t *testing.T) {
	f := &nativeFixture{}
	s := newNativeStore(t, f)
	f.mu.Lock()
	f.class.Description = vectorstore.SpaceID("different").StorageKey()
	f.mu.Unlock()
	err := s.Upsert(t.Context(), fixtureBatch([]*document.Document{{ID: "00000000-0000-0000-0000-000000000001", Text: "text"}}))
	if !errors.Is(err, vectorstore.ErrIncompatibleSpace) || f.batches.Load() != 0 {
		t.Fatalf("err=%v writes=%d", err, f.batches.Load())
	}
}
