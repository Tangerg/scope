package couchbase

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
)

func TestStoredRecordRoundTrip(t *testing.T) {
	for _, facts := range []metadata.Map{nil, {}, {"integer": json.RawMessage(`9007199254740993`), "huge": json.RawMessage(`1e400`), "tiny": json.RawMessage(`1e-400`), "nested": json.RawMessage(`{"items":[1e400]}`), "escaped": json.RawMessage(`"\u0041lice"`)}} {
		body, err := encodeStoredDocument("text", facts, []float64{1, 0})
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]json.RawMessage
		if err = jsonv2.Unmarshal(body, &wire); err != nil {
			t.Fatal(err)
		}
		if len(wire) != 3 || wire["id"] != nil {
			t.Fatal("record duplicates KV identity")
		}
		var encoded string
		if err = jsonv2.Unmarshal(wire["metadata"], &encoded); err != nil {
			t.Fatal(err)
		}
		want, err := facts.MarshalJSON()
		if err != nil || encoded != string(want) {
			t.Fatalf("encoded fact changed: %s, %v", encoded, err)
		}
		row, err := jsonv2.Marshal(struct {
			ID     string          `json:"id"`
			CAS    uint64          `json:"cas"`
			Record json.RawMessage `json:"record"`
		}{"id\x00 ", 1791183817835020288, body})
		if err != nil {
			t.Fatal(err)
		}
		candidate, err := decodeStoredRow(row)
		if err != nil {
			t.Fatal(err)
		}
		var projected metadata.Map
		if err := projected.UnmarshalJSON(want); err != nil {
			t.Fatal(err)
		}
		if candidate.document.ID != "id\x00 " || candidate.document.Text != "text" || uint64(candidate.cas) != 1791183817835020288 || !slices.Equal(candidate.embedding, []float64{1, 0}) || !candidate.document.Metadata.Equal(projected) || (facts == nil) != (candidate.document.Metadata == nil) {
			t.Fatalf("projection changed: %+v", candidate)
		}
	}
}

func TestStoredRecordRejectsInvalidAndObsoleteFormats(t *testing.T) {
	for _, raw := range []string{
		`null`, `{}`, `{"id":"key","cas":1,"record":{"id":"other","content":"text","metadata":"{}","embedding":[1,0]}}`,
		`{"id":"key","cas":1,"record":{"content":"text","metadata":{},"embedding":[1,0]}}`,
		`{"id":"key","cas":1,"record":{"content":"text","metadata":"not JSON","embedding":[1,0]}}`,
		`{"id":"key","cas":0,"record":{"content":"text","metadata":"{}","embedding":[1,0]}}`,
		`{"id":"","cas":1,"record":{"content":"text","metadata":"{}","embedding":[1,0]}}`,
		`{"id":"key","cas":1,"record":{"content":"","metadata":"{}","embedding":[1,0]}}`,
		`{"id":"key","cas":1,"record":{"content":"text","metadata":"{}","embedding":[]}}`,
		`{"id":"key","cas":1,"record":{"content":"text","metadata":"{}","embedding":[null,0]}}`,
		`{"id":"key","cas":1,"record":{"content":"text","metadata":"{}","embedding":[1e400,0]}}`,
		`{"id":"key","cas":1,"record":{"content":"text","embedding":[1,0]}}`,
	} {
		if _, err := decodeStoredRow([]byte(raw)); err == nil {
			t.Fatalf("accepted invalid record %s", raw)
		}
	}
	if _, err := encodeStoredDocument("text", metadata.Map{"broken": json.RawMessage(`{`)}, []float64{1}); err == nil {
		t.Fatal("accepted broken metadata")
	}
	if _, err := encodeStoredDocument("text", nil, []float64{math.Inf(1)}); err == nil {
		t.Fatal("accepted non-finite vector")
	}
}

func TestNativeKeyLimits(t *testing.T) {
	s := &Store{maximumKeyBytes: maximumCollectionKeyBytes}
	for _, id := range []string{"ID", "id", "id\x00", "id ", "中文", strings.Repeat("x", 246)} {
		if err := s.validateKey(id); err != nil {
			t.Fatalf("valid key rejected: %v", err)
		}
	}
	for _, id := range []string{"", strings.Repeat("x", 247)} {
		if err := s.validateKey(id); !errors.Is(err, vectorstore.ErrInvalidDocument) {
			t.Fatalf("invalid key accepted: %v", err)
		}
	}
	s.maximumKeyBytes = maximumDefaultKeyBytes
	if err := s.validateKey(strings.Repeat("x", 250)); err != nil {
		t.Fatal(err)
	}
	if err := s.validateKey(strings.Repeat("x", 251)); err == nil {
		t.Fatal("oversized default key accepted")
	}
}

func TestNativeDistanceScores(t *testing.T) {
	for _, test := range []struct {
		metric         Similarity
		distance, want float64
	}{
		{SimilarityCosine, 0, 1}, {SimilarityCosine, 1, .5}, {SimilarityCosine, 2, 0},
		{SimilarityL2Norm, 0, 1}, {SimilarityL2Norm, 1, .5},
		{SimilarityDotProduct, 0, .5}, {SimilarityDotProduct, -1, 1 / (1 + math.Exp(-1))},
	} {
		if got := test.metric.score(test.distance); math.Abs(got.Float64()-test.want) > 1e-12 {
			t.Fatalf("%s distance %v: %v", test.metric, test.distance, got)
		}
	}
	for _, metric := range []Similarity{SimilarityCosine, SimilarityL2Norm, SimilarityDotProduct} {
		if !metric.Valid() || metric.String() != string(metric) {
			t.Fatal("invalid native metric")
		}
		for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
			if err := metric.score(value).Validate(); !errors.Is(err, vectorstore.ErrInvalidScore) {
				t.Fatal("non-finite distance became success")
			}
		}
	}
	if Similarity("invalid").Valid() {
		t.Fatal("invalid metric accepted")
	}
}

func TestResultOrdering(t *testing.T) {
	hit := func(id string, distance float64) rankedResult {
		return rankedResult{result: &vectorstore.SearchResult{Document: &document.Document{ID: id, Text: "text"}, Score: vectorstore.ScoreFromCosineDistance(distance)}, distance: distance}
	}
	rows := []rankedResult{hit("z", 1), hit("id ", 0), hit("id", 0), hit("ID", 0)}
	slices.SortFunc(rows, compareResults)
	if got := []string{rows[0].result.Document.ID, rows[1].result.Document.ID, rows[2].result.Document.ID, rows[3].result.Document.ID}; !slices.Equal(got, []string{"ID", "id", "id ", "z"}) {
		t.Fatal(got)
	}
	near := hit("z_near", -1000)
	far := hit("a_far", -100)
	near.result.Score = SimilarityDotProduct.score(near.distance)
	far.result.Score = SimilarityDotProduct.score(far.distance)
	if near.result.Score != far.result.Score || compareResults(near, far) >= 0 {
		t.Fatal("rounded score replaced native ranking authority")
	}
}

func TestInvalidStoreCallsDoNotReachDependencies(t *testing.T) {
	s := &Store{maximumKeyBytes: maximumCollectionKeyBytes}
	if err := s.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: strings.Repeat("x", 251), Text: "text"}}}); err == nil {
		t.Fatal("oversized ID accepted")
	}
	if err := s.DeleteIDs(t.Context(), []string{"valid", strings.Repeat("x", 251)}); err == nil {
		t.Fatal("invalid key batch mutated")
	}
	if response, err := s.Search(t.Context(), &vectorstore.SearchRequest{Query: "text", Options: vectorstore.SearchOptions{Mode: vectorstore.SearchModeHybrid}}); response != nil || !errors.Is(err, vectorstore.ErrUnsupportedSearchMode) {
		t.Fatalf("hybrid accepted: %v", err)
	}
}
