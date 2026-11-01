package weaviate

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/weaviate/weaviate/entities/models"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestCoreMetadataSurvivesNativeStringsExactly(t *testing.T) {
	for _, facts := range []metadata.Map{nil, {}, {"huge": json.RawMessage(`1e1000`), "long": json.RawMessage(`1.00000000000000001`), "$native.key": json.RawMessage(`{"nested":[null,{},9007199254740993]}`), "content": json.RawMessage(`"business value"`)}} {
		doc := &document.Document{ID: "f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4", Text: "stored text🙂", Metadata: facts}
		store, _ := indexedNativeStore(t, []*document.Document{doc})
		response, err := fixtureQuery(t.Context(), store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("q")})
		if err != nil || len(response.Results) != 1 {
			t.Fatalf("response=%v error=%v", response, err)
		}
		got := response.Results[0].Document
		if got.ID != doc.ID || got.Text != doc.Text || !got.Metadata.Equal(facts) || (got.Metadata == nil) != (facts == nil) {
			t.Fatalf("native metadata changed: %v", got)
		}
	}
}

func TestLaterInvalidCoordinatesPreventEveryWrite(t *testing.T) {
	for _, bad := range [][]float64{{1}, {math.MaxFloat64, 0}} {
		f := &nativeFixture{}
		s := newNativeStore(t, f)
		b := fixtureBatch([]*document.Document{{ID: "00000000-0000-0000-0000-000000000001", Text: "text"}, {ID: "00000000-0000-0000-0000-000000000002", Text: "text"}})
		b.Records[1].Values = bad
		if err := s.Upsert(t.Context(), b); err == nil || f.batches.Load() != 0 || f.scans != 0 {
			t.Fatalf("err=%v writes=%d scans=%d", err, f.batches.Load(), f.scans)
		}
	}
}
func TestNativeHitsAreCheckedAgainstTheirOriginalMetadata(t *testing.T) {
	for _, corrupt := range []func(*models.Object){
		func(o *models.Object) { o.Properties.(map[string]any)[fieldMetadata] = `{"key":1,"key":2}` },
		func(o *models.Object) { o.Properties.(map[string]any)[fieldMetadata] = `{"v":"no"}` },
		func(o *models.Object) { o.Vector = nil },
	} {
		id := "f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4"
		s, f := indexedNativeStore(t, []*document.Document{{ID: id, Text: "text", Metadata: metadata.Map{"v": json.RawMessage(`"x"`)}}})
		corrupt(f.objects[id])
		r, err := s.Query(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("q"), Options: vectorstore.SearchOptions{Filter: filter.EQ("v", "x"), MinScore: 1}})
		if err == nil || r != nil {
			t.Fatalf("invalid native hit=%v %v", r, err)
		}
	}
}
func TestNativeHybridScoresAndInvalidHitsNeverReturnPartialSuccess(t *testing.T) {
	for _, fault := range []string{"score range", "score number", "score NaN", "wrong vector", "duplicate", "surplus", "query failure"} {
		t.Run(fault, func(t *testing.T) {
			store, fixture := indexedNativeStore(t, []*document.Document{{ID: "f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4", Text: "text"}})
			fixture.queryChange = func(items []map[string]any) []map[string]any {
				additional := items[0]["_additional"].(map[string]any)
				switch fault {
				case "score range":
					additional[additionalScore] = "2"
				case "score number":
					additional[additionalScore] = .5
				case "score NaN":
					additional[additionalScore] = "NaN"
				case "wrong vector":
					additional[additionalVector] = nil
				case "duplicate":
					items = append(items, items[0])
				case "surplus":
					items = append(items, items[0], items[0])
				}
				return items
			}
			fixture.queryError = fault == "query failure"
			response, err := fixtureQuery(t.Context(), store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("q"), Options: vectorstore.SearchOptions{Mode: vectorstore.SearchModeHybrid, TopK: 2, MinScore: 1}})
			if err == nil || response != nil {
				t.Fatalf("invalid hit became success: %v %v", response, err)
			}
		})
	}
}
