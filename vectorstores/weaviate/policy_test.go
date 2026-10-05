package weaviate

import (
	"testing"

	"github.com/weaviate/weaviate/entities/models"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
)

func TestNativeCollectionOwnsMetricWithoutConfiguredCounterpart(t *testing.T) {
	for _, metric := range []string{distanceCosine, distanceDot, distanceL2Squared, distanceHamming, distanceManhattan} {
		store, calls := newNativeStore(t, &nativeFixture{class: nativeClass("Documents", metric)})
		if store.schema.metric != metric || calls.Load() != 0 {
			t.Fatalf("metric=%s model calls=%d", store.schema.metric, calls.Load())
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
			if err := schema.read(class, "Documents"); err == nil {
				t.Fatal("invalid native schema was accepted")
			}
		})
	}
}

func TestObservedNativeWidthConstrainsPublication(t *testing.T) {
	store, fixture := indexedNativeStore(t, []*document.Document{{ID: "f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4", Text: "native"}})
	fixture.objects["f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4"].Vector = []float32{1, 0, 0}
	before := fixture.batches.Load()
	err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "00000000-0000-0000-0000-000000000001", Text: "wrong width"}}})
	if err == nil || fixture.batches.Load() != before {
		t.Fatal("wrong width reached native publication")
	}
}
