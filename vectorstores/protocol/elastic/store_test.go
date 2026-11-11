package elastic

import (
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
)

type fakeEngine struct{ schema func(dimensions int) Schema }

func (fakeEngine) Name() string { return "fake" }

func (fakeEngine) SettingNames() []string { return []string{"index.fake"} }

func (f fakeEngine) Admit(embedding jsontext.Value, _ IndexSettings) (Schema, error) {
	var field struct {
		Dimensions int `json:"dims"`
	}
	if err := jsonv2.Unmarshal(embedding, &field); err != nil {
		return nil, err
	}
	return f.schema(field.Dimensions), nil
}

type fakeSchema struct{ dimensions int }

func (f fakeSchema) ValidateIndexedVector(vector []float32) error {
	return f.ValidateQueryVector(vector)
}

func (f fakeSchema) ValidateQueryVector(vector []float32) error {
	if len(vector) != f.dimensions {
		return errors.New("fake: dimension mismatch")
	}
	return nil
}

func (fakeSchema) Score(raw float64) (vectorstore.Score, error) {
	return vectorstore.ScoreFromValue(raw), nil
}

func (fakeSchema) CheckFilteredSearch() error { return nil }

func (fakeSchema) SearchSyntax(neighbors *Neighbors) (jsontext.Value, error) {
	syntax := struct {
		Source bool       `json:"_source"`
		KNN    *Neighbors `json:"fake_knn,omitzero"`
	}{Source: true, KNN: neighbors}
	return jsonv2.Marshal(syntax)
}

// listSchema cannot be compared, so Store cannot detect its change.
type listSchema struct {
	fakeSchema
	_ []string
}

// fakeIndex serves one index whose embedding dimension a test can change.
type fakeIndex struct {
	t          *testing.T
	dimensions int
	paths      []string
	searches   []string
}

func (f *fakeIndex) perform(request *http.Request) (*http.Response, error) {
	f.paths = append(f.paths, request.Method+" "+request.URL.Path)
	var body string
	switch {
	case strings.HasSuffix(request.URL.Path, "/_mapping"):
		body = fmt.Sprintf(`{"documents":{"mappings":{"_meta":{"scope_vector_space":"space"},"dynamic":"strict","properties":{"content":{"type":"text"},"metadata_json":{"type":"keyword","index":false,"doc_values":false},"embedding":{"dims":%d}}}}}`, f.dimensions)
	case strings.HasSuffix(request.URL.Path, "/_settings"):
		body = `{"documents":{"settings":{},"defaults":{"index.max_result_window":"100"}}}`
	case strings.HasSuffix(request.URL.Path, "/_search"):
		raw, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		f.searches = append(f.searches, string(raw))
		body = `{"timed_out":false,"_shards":{"total":1,"successful":1,"failed":0},"hits":{"total":{"value":0,"relation":"eq"},"hits":[]}}`
	default:
		f.t.Errorf("unexpected request %s %s", request.Method, request.URL)
		return &http.Response{StatusCode: http.StatusInternalServerError, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {mediaTypeJSON}}, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func newFakeStore(t *testing.T, index *fakeIndex, schema func(int) Schema) (*Store, error) {
	t.Helper()
	return NewStore(t.Context(), StoreConfig{Engine: fakeEngine{schema: schema}, Perform: index.perform, IndexName: "documents", Space: "space"})
}

func comparableSchema(dimensions int) Schema { return fakeSchema{dimensions: dimensions} }

func TestStoreReadmitsTheIndexBeforeEveryMutation(t *testing.T) {
	index := &fakeIndex{t: t, dimensions: 2}
	store, err := newFakeStore(t, index, comparableSchema)
	if err != nil {
		t.Fatal(err)
	}
	index.dimensions = 3
	batch := &vectorstore.Batch{Space: "space", Records: []vectorstore.Record{{Document: &document.Document{ID: "one", Text: "text"}, Values: []float64{1, 0}}}}
	if err := store.Upsert(t.Context(), batch); !errors.Is(err, ErrIncompatibleIndex) {
		t.Fatalf("Upsert = %v, want ErrIncompatibleIndex after the mapping changed", err)
	}
	for _, path := range index.paths {
		if strings.Contains(path, bulkPath) {
			t.Fatalf("a changed index received a write: %v", index.paths)
		}
	}
}

func TestStoreJoinsItsSearchMembersWithEngineSyntax(t *testing.T) {
	index := &fakeIndex{t: t, dimensions: 2}
	store, err := newFakeStore(t, index, comparableSchema)
	if err != nil {
		t.Fatal(err)
	}
	response, err := store.Query(t.Context(), &vectorstore.Query{Space: "space", Values: []float64{1, 0}})
	if err != nil || len(response.Results) != 0 {
		t.Fatalf("Query = %#v, %v", response, err)
	}
	if len(index.searches) != 1 {
		t.Fatalf("searches = %v", index.searches)
	}
	var body map[string]jsontext.Value
	if err := jsonv2.Unmarshal([]byte(index.searches[0]), &body); err != nil {
		t.Fatal(err)
	}
	for member, want := range map[string]string{
		"size": fmt.Sprint(vectorstore.SearchOptions{}.ResultLimit()), "track_total_hits": `true`, "seq_no_primary_term": `true`, "stored_fields": `["_routing"]`, "_source": `true`,
	} {
		if string(body[member]) != want {
			t.Fatalf("%s = %s, want %s in %s", member, body[member], want, index.searches[0])
		}
	}
	if _, found := body["fake_knn"]; !found {
		t.Fatalf("engine syntax missing from %s", index.searches[0])
	}
}

func TestStoreRequiresAComparableSchema(t *testing.T) {
	_, err := newFakeStore(t, &fakeIndex{t: t, dimensions: 2}, func(dimensions int) Schema {
		return listSchema{fakeSchema: fakeSchema{dimensions: dimensions}}
	})
	if err == nil || !strings.Contains(err.Error(), "not comparable") {
		t.Fatalf("NewStore = %v, want rejection of a schema it cannot compare", err)
	}
}
