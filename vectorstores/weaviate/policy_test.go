package weaviate

import (
	"errors"
	"testing"

	"github.com/weaviate/weaviate/entities/models"

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
	for name, change := range map[string]func(*models.Class){
		"class alias":      func(class *models.Class) { class.Class = "Other" },
		"vectorizer":       func(class *models.Class) { class.Vectorizer = "text2vec-native" },
		"named vector":     func(class *models.Class) { class.VectorConfig = map[string]models.VectorConfig{"named": {}} },
		"tenant":           func(class *models.Class) { class.MultiTenancyConfig = &models.MultiTenancyConfig{Enabled: true} },
		"missing policy":   func(class *models.Class) { class.VectorIndexConfig = nil },
		"missing distance": func(class *models.Class) { class.VectorIndexConfig = map[string]any{} },
		"unknown distance": func(class *models.Class) { class.VectorIndexConfig = map[string]any{"distance": "unknown"} },
		"skipped index":    func(class *models.Class) { class.VectorIndexConfig.(map[string]any)["skip"] = true },
		"old projections": func(class *models.Class) {
			class.Properties = append(class.Properties, &models.Property{Name: "metadata_tenant", DataType: []string{"text"}})
		},
		"word metadata":         func(class *models.Class) { class.Properties[1].Tokenization = "word" },
		"unfilterable metadata": func(class *models.Class) { class.Properties[1].IndexFilterable = new(false) },
		"unsearchable content":  func(class *models.Class) { class.Properties[0].IndexSearchable = new(false) },
	} {
		t.Run(name, func(t *testing.T) {
			class := nativeClass("Documents", distanceCosine)
			change(class)
			var schema nativeSchema
			if err := schema.read(class, "Documents", fixtureSpace); err == nil {
				t.Fatal("invalid native schema was accepted")
			}
		})
	}
}

func TestWrongSpaceReprovisioningPreventsWrite(t *testing.T) {
	f := &nativeFixture{}
	s := newNativeStore(t, f)
	f.class.Description = vectorstore.SpaceID("different").StorageKey()
	err := s.Upsert(t.Context(), fixtureBatch([]*document.Document{{ID: "00000000-0000-0000-0000-000000000001", Text: "text"}}))
	if !errors.Is(err, vectorstore.ErrIncompatibleSpace) || f.batches.Load() != 0 {
		t.Fatalf("err=%v writes=%d", err, f.batches.Load())
	}
}
