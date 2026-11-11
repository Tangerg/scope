package pinecone

import (
	"context"
	"errors"
	"math"
	"testing"

	pineconesdk "github.com/pinecone-io/go-pinecone/v7/pinecone"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestReadsRejectEveryIncompleteCurrentRecord(t *testing.T) {
	mutations := map[string]func(*pineconesdk.Vector){
		"missing content":     func(point *pineconesdk.Vector) { delete(point.Metadata.Fields, contentField) },
		"missing metadata":    func(point *pineconesdk.Vector) { delete(point.Metadata.Fields, metadataField) },
		"legacy extra field":  func(point *pineconesdk.Vector) { point.Metadata.Fields["value"] = structpb.NewStringValue("a") },
		"wrong content type":  func(point *pineconesdk.Vector) { point.Metadata.Fields[contentField] = structpb.NewNumberValue(1) },
		"wrong metadata type": func(point *pineconesdk.Vector) { point.Metadata.Fields[metadataField] = structpb.NewNullValue() },
		"invalid metadata": func(point *pineconesdk.Vector) {
			point.Metadata.Fields[metadataField] = structpb.NewStringValue(`{"x":1,"x":2}`)
		},
		"nonobject metadata":      func(point *pineconesdk.Vector) { point.Metadata.Fields[metadataField] = structpb.NewStringValue(`[]`) },
		"missing values":          func(point *pineconesdk.Vector) { point.Values = nil },
		"wrong dimensions":        func(point *pineconesdk.Vector) { point.Values = new([]float32{1}) },
		"nonfinite vector":        func(point *pineconesdk.Vector) { point.Values = new([]float32{float32(math.Inf(1)), 0}) },
		"sparse values":           func(point *pineconesdk.Vector) { point.SparseValues = &pineconesdk.SparseValues{} },
		"missing metadata object": func(point *pineconesdk.Vector) { point.Metadata = nil },
		"empty text":              func(point *pineconesdk.Vector) { point.Metadata.Fields[contentField] = structpb.NewStringValue("") },
		"native invalid ID":       func(point *pineconesdk.Vector) { point.Id = "🙂" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			store, native := fixtureStore(t)
			installDocuments(t, store, &document.Document{ID: "one", Text: "text"}, &document.Document{ID: "two", Text: "text"})
			mutate(native.points["two"])
			response, err := store.Query(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("q"), Options: vectorstore.SearchOptions{MinScore: 1}})
			if err == nil || response != nil || len(native.queries) != 1 {
				t.Fatalf("corrupt source became apparent empty success: %v, %v", response, err)
			}
			if err = store.DeleteWhere(t.Context(), filter.IsNull("absent")); err == nil || len(native.points) != 2 {
				t.Fatalf("corrupt source permitted deletion: %v", err)
			}
		})
	}
}

func TestUnsupportedPredicatesDoNotQueryOrDelete(t *testing.T) {
	store, native := fixtureStore(t)
	installDocuments(t, store, &document.Document{ID: "one", Text: "text"})
	for _, p := range []filter.Predicate{filter.EQ("value", uint64(9007199254740993)), filter.IsNull("tenant")} {
		q := &vectorstore.Query{Space: fixtureSpace, Values: []float64{1, 0}, Options: vectorstore.SearchOptions{Filter: p}}
		if hits, err := store.Query(t.Context(), q); hits != nil || !errors.Is(err, vectorstore.ErrUnsupportedFilter) {
			t.Fatalf("query=%v err=%v", hits, err)
		}
		if err := store.DeleteWhere(t.Context(), p); !errors.Is(err, vectorstore.ErrUnsupportedFilter) {
			t.Fatal(err)
		}
	}
	if len(native.queries) != 0 || len(native.points) != 1 {
		t.Fatal("unsupported predicate reached native ranking or deletion")
	}
}

func TestSearchDoesNotHideMalformedLowScoreHits(t *testing.T) {
	for _, failure := range []string{"nil response", "query error", "wrong namespace", "too many hits", "nil hit", "nil vector", "duplicate hit", "bad metadata", "changed membership", "NaN score", "negative distance"} {
		t.Run(failure, func(t *testing.T) {
			store, native := fixtureStore(t)
			installDocuments(t, store, &document.Document{ID: "one", Text: "text", Metadata: metadata.Map{"value": []byte(`"a"`)}})
			if failure == "negative distance" {
				store.binding.schema.metric, native.metric = pineconesdk.IndexMetricEuclidean, pineconesdk.IndexMetricEuclidean
			}
			native.query = func(*pineconesdk.QueryByVectorValuesRequest) (*pineconesdk.QueryVectorsResponse, error) {
				point := clonePoint(native.points["one"])
				page := &pineconesdk.QueryVectorsResponse{Namespace: native.Namespace(), Matches: []*pineconesdk.ScoredVector{{Vector: point, Score: 0}}}
				switch failure {
				case "nil response":
					return nil, nil
				case "query error":
					return nil, errNativeFailure
				case "wrong namespace":
					page.Namespace = "other"
				case "too many hits":
					page.Matches = append(page.Matches, page.Matches[0], page.Matches[0])
				case "nil hit":
					page.Matches[0] = nil
				case "nil vector":
					page.Matches[0].Vector = nil
				case "duplicate hit":
					page.Matches = append(page.Matches, page.Matches[0])
				case "bad metadata":
					point.Metadata.Fields[metadataField] = structpb.NewStringValue(`[]`)
				case "changed membership":
					point.Metadata.Fields[metadataField] = structpb.NewStringValue(`{"value":"other"}`)
				case "NaN score":
					page.Matches[0].Score = float32(math.NaN())
				case "negative distance":
					page.Matches[0].Score = -1
				}
				return page, nil
			}
			response, err := store.Query(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("q"), Options: vectorstore.SearchOptions{TopK: 2, MinScore: 1, Filter: filter.EQ("value", "a")}})
			if err == nil || response != nil || len(native.queries) != 1 {
				t.Fatalf("malformed low score hit was not rejected at the native response boundary: %v, %v", response, err)
			}
		})
	}
}

func TestVectorQueryAndEmptyResultContracts(t *testing.T) {
	store, native := fixtureStore(t)
	if response, err := store.Query(t.Context(), nil); response != nil || err == nil {
		t.Fatal("nil request succeeded")
	}
	if response, err := store.Query(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("q"), Options: vectorstore.SearchOptions{TopK: MaxTopK + 1}}); response != nil || !errors.Is(err, vectorstore.ErrInvalidOptions) {
		t.Fatalf("TopK: %v, %v", response, err)
	}
	// An unfiltered Search issues the native query directly instead of listing the
	// namespace first, so it runs exactly one query and returns no results when the
	// namespace is empty.
	native.query = func(*pineconesdk.QueryByVectorValuesRequest) (*pineconesdk.QueryVectorsResponse, error) {
		return &pineconesdk.QueryVectorsResponse{Namespace: native.Namespace()}, nil
	}
	response, err := store.Query(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("q")})
	if err != nil || response == nil || len(response.Results) != 0 || len(native.queries) != 1 {
		t.Fatalf("empty namespace: %v, %v", response, err)
	}
	installDocuments(t, store, &document.Document{ID: "one", Text: "text"})
	if response, err = store.Query(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("q")}); err != nil || response == nil || len(response.Results) != 0 {
		t.Fatalf("empty query: %v, %v", response, err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	// A filtered Search reaches the selection path that observes cancellation.
	if response, err = store.Query(canceled, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("q"), Options: vectorstore.SearchOptions{Filter: filter.EQ("value", "a")}}); response != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v, %v", response, err)
	}
}

func TestQueryAdmissionRejectsBeforeNativePolicyReads(t *testing.T) {
	for _, sample := range []struct {
		name    string
		space   vectorstore.SpaceID
		options vectorstore.SearchOptions
		want    error
	}{
		{name: "foreign space", space: "other", want: vectorstore.ErrIncompatibleSpace},
		{name: "negative limit", space: fixtureSpace, options: vectorstore.SearchOptions{TopK: -1}, want: vectorstore.ErrInvalidOptions},
		{name: "native limit", space: fixtureSpace, options: vectorstore.SearchOptions{TopK: MaxTopK + 1}, want: vectorstore.ErrInvalidOptions},
		{name: "hybrid mode", space: fixtureSpace, options: vectorstore.SearchOptions{Mode: vectorstore.SearchModeHybrid}, want: vectorstore.ErrUnsupportedSearchMode},
		{name: "unsupported filter", space: fixtureSpace, options: vectorstore.SearchOptions{Filter: filter.EQ("tenant", 1)}, want: vectorstore.ErrUnsupportedFilter},
	} {
		t.Run(sample.name, func(t *testing.T) {
			store, native := fixtureStore(t)
			if err := store.CheckQuery(t.Context(), sample.space, sample.options); !errors.Is(err, sample.want) {
				t.Fatalf("preflight: %v", err)
			}
			request := &vectorstore.Query{Space: sample.space, Values: []float64{1, 0}, Options: sample.options}
			if sample.options.Mode == vectorstore.SearchModeHybrid {
				request.LexicalText = "query"
			}
			if response, err := store.Query(t.Context(), request); response != nil || !errors.Is(err, sample.want) {
				t.Fatalf("query: %v, %v", response, err)
			}
			if native.describes != 0 || len(native.queries) != 0 {
				t.Fatal("invalid query reached native policy or data plane")
			}
		})
	}
}

func TestNativeBindingDriftCannotRedirectOrReinterpretQueries(t *testing.T) {
	for _, sample := range []struct {
		name   string
		mutate func(*pineconesdk.Index)
		want   error
	}{
		{name: "space", mutate: func(index *pineconesdk.Index) { (*index.Tags)[SpaceTag] = "other" }, want: vectorstore.ErrIncompatibleSpace},
		{name: "host", mutate: func(index *pineconesdk.Index) { index.Host = "other.fixture" }, want: ErrIncompatibleIndex},
		{name: "dimension", mutate: func(index *pineconesdk.Index) { index.Schema.Fields[vectorField].DenseVector.Dimension = 3 }, want: ErrIncompatibleIndex},
		{name: "metric", mutate: func(index *pineconesdk.Index) {
			index.Schema.Fields[vectorField].DenseVector.Metric = pineconesdk.IndexMetricEuclidean
		}, want: ErrIncompatibleIndex},
	} {
		t.Run(sample.name, func(t *testing.T) {
			store, native := fixtureStore(t)
			opening := store.binding
			native.describe = func() (*pineconesdk.Index, error) {
				index := declaredIndex()
				sample.mutate(index)
				return index, nil
			}
			if err := store.CheckQuery(t.Context(), fixtureSpace, vectorstore.SearchOptions{}); !errors.Is(err, sample.want) {
				t.Fatalf("preflight: %v", err)
			}
			response, err := store.Query(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: []float64{1, 0}})
			if response != nil || !errors.Is(err, sample.want) || len(native.queries) != 0 {
				t.Fatalf("query accepted drift: %v, %v", response, err)
			}
			if store.binding != opening {
				t.Fatal("drift changed the opening binding")
			}
		})
	}
}
