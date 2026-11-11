package weaviate

import (
	"encoding/json"
	"math"
	"testing"

	pb "github.com/weaviate/weaviate/grpc/generated/protocol/v1"
	"google.golang.org/protobuf/types/known/structpb"

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
		if err := s.Upsert(t.Context(), b); err == nil || f.batches.Load() != 0 || f.scans.Load() != 0 {
			t.Fatalf("err=%v writes=%d scans=%d", err, f.batches.Load(), f.scans.Load())
		}
	}
}

func TestNativeHitsAreCheckedAgainstTheirOriginalMetadata(t *testing.T) {
	for _, corrupt := range []func(*pb.BatchObject){
		func(o *pb.BatchObject) {
			o.Properties.NonRefProperties.Fields[fieldMetadata] = structpb.NewStringValue(`{"key":1,"key":2}`)
		},
		func(o *pb.BatchObject) {
			o.Properties.NonRefProperties.Fields[fieldMetadata] = structpb.NewStringValue(`{"v":"no"}`)
		},
		func(o *pb.BatchObject) { o.Vectors = nil },
	} {
		id := "f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4"
		s, f := indexedNativeStore(t, []*document.Document{{ID: id, Text: "text", Metadata: metadata.Map{"v": json.RawMessage(`"x"`)}}})
		f.mu.Lock()
		corrupt(f.objects[id])
		f.mu.Unlock()
		r, err := s.Query(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("q"), Options: vectorstore.SearchOptions{Filter: filter.EQ("v", "x"), MinScore: 1}})
		if err == nil || r != nil {
			t.Fatalf("invalid native hit=%v %v", r, err)
		}
	}
}

func TestNativeHybridScoresAndInvalidHitsNeverReturnPartialSuccess(t *testing.T) {
	for _, fault := range []string{"score range", "missing score", "score NaN", "wrong vector", "duplicate", "surplus", "query failure"} {
		t.Run(fault, func(t *testing.T) {
			store, fixture := indexedNativeStore(t, []*document.Document{{ID: "f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4", Text: "text"}})
			fixture.mu.Lock()
			fixture.queryChange = func(items []*pb.SearchResult) []*pb.SearchResult {
				additional := items[0].Metadata
				switch fault {
				case "score range":
					additional.Score = 2
				case "missing score":
					additional.ScorePresent = false
				case "score NaN":
					additional.Score = float32(math.NaN())
				case "wrong vector":
					additional.Vectors = nil
				case "duplicate":
					items = append(items, items[0])
				case "surplus":
					items = append(items, items[0], items[0])
				}
				return items
			}
			fixture.queryError = fault == "query failure"
			fixture.mu.Unlock()
			response, err := fixtureQuery(t.Context(), store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("q"), Options: vectorstore.SearchOptions{Mode: vectorstore.SearchModeHybrid, TopK: 2, MinScore: 1}})
			if err == nil || response != nil {
				t.Fatalf("invalid hit became success: %v %v", response, err)
			}
		})
	}
}

func TestSemanticHitWithoutMeasuredDistanceFails(t *testing.T) {
	f := &nativeFixture{queryChange: func(rows []*pb.SearchResult) []*pb.SearchResult {
		rows[0].Metadata.DistancePresent = false
		return rows
	}}
	s := newNativeStore(t, f)
	if err := s.Upsert(t.Context(), fixtureBatch([]*document.Document{{ID: "00000000-0000-4000-8000-000000000001", Text: "text"}})); err != nil {
		t.Fatal(err)
	}
	response, err := s.Query(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("q")})
	if response != nil || err == nil {
		t.Fatalf("missing distance became a perfect match: %v %v", response, err)
	}
}
