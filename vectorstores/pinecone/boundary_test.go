package pinecone

import (
	"context"
	"errors"
	"math"
	"testing"

	pineconesdk "github.com/pinecone-io/go-pinecone/v4/pinecone"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/Tangerg/scope/core/document"
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
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q", Options: vectorstore.SearchOptions{MinScore: 1, Filter: filter.EQ("absent", "value")}})
			if err == nil || response != nil || len(native.queries) != 0 {
				t.Fatalf("corrupt source became apparent empty success: %v, %v", response, err)
			}
			if err = store.DeleteWhere(t.Context(), filter.IsNull("absent")); err == nil || len(native.points) != 2 {
				t.Fatalf("corrupt source permitted deletion: %v", err)
			}
		})
	}
}

func TestSelectionFailuresNeverPublishDeletionOrQuery(t *testing.T) {
	for _, failure := range []string{"nil list", "wrong namespace", "nil ID", "empty ID", "duplicate ID", "oversized list", "repeat token", "list error", "nil fetch", "missing fetch", "wrong fetch namespace", "wrong fetched identity", "fetch error"} {
		t.Run(failure, func(t *testing.T) {
			store, native := fixtureStore(t)
			installDocuments(t, store, &document.Document{ID: "one", Text: "text"})
			native.list = func(*pineconesdk.ListVectorsRequest) (*pineconesdk.ListVectorsResponse, error) {
				page := &pineconesdk.ListVectorsResponse{Namespace: native.Namespace(), VectorIds: []*string{new("one")}}
				switch failure {
				case "nil list":
					return nil, nil
				case "wrong namespace":
					page.Namespace = "other"
				case "nil ID":
					page.VectorIds = []*string{nil}
				case "empty ID":
					page.VectorIds = []*string{new("")}
				case "duplicate ID":
					page.VectorIds = append(page.VectorIds, new("one"))
				case "oversized list":
					page.VectorIds = make([]*string, metadataListPageSize+1)
				case "repeat token":
					page.VectorIds = nil
					page.NextPaginationToken = new("repeated")
				case "list error":
					return nil, errNativeFailure
				}
				return page, nil
			}
			native.fetch = func([]string) (*pineconesdk.FetchVectorsResponse, error) {
				page := &pineconesdk.FetchVectorsResponse{Namespace: native.Namespace(), Vectors: map[string]*pineconesdk.Vector{"one": clonePoint(native.points["one"])}}
				switch failure {
				case "nil fetch":
					return nil, nil
				case "missing fetch":
					delete(page.Vectors, "one")
				case "wrong fetch namespace":
					page.Namespace = "other"
				case "wrong fetched identity":
					page.Vectors["one"].Id = "other"
				case "fetch error":
					return nil, errNativeFailure
				}
				return page, nil
			}
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q"})
			if err == nil || response != nil || len(native.queries) != 0 {
				t.Fatalf("response=%v error=%v", response, err)
			}
			if err = store.DeleteWhere(t.Context(), filter.IsNull("value")); err == nil || native.points["one"] == nil {
				t.Fatalf("deleted with incomplete selection: %v", err)
			}
		})
	}
}

func TestSearchDoesNotHideMalformedLowScoreHits(t *testing.T) {
	for _, failure := range []string{"nil response", "query error", "wrong namespace", "too many hits", "nil hit", "nil vector", "duplicate hit", "bad metadata", "changed membership", "NaN score", "negative distance"} {
		t.Run(failure, func(t *testing.T) {
			store, native := fixtureStore(t)
			installDocuments(t, store, &document.Document{ID: "one", Text: "text"})
			if failure == "negative distance" {
				store.schema.metric = pineconesdk.Euclidean
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
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q", Options: vectorstore.SearchOptions{TopK: 2, MinScore: 1, Filter: filter.IsNull("value")}})
			if err == nil || response != nil {
				t.Fatalf("malformed low score hit succeeded: %v, %v", response, err)
			}
		})
	}
}

func TestSearchRequestAndEmptyResultContracts(t *testing.T) {
	store, native := fixtureStore(t)
	if response, err := store.Search(t.Context(), nil); response != nil || err == nil {
		t.Fatal("nil request succeeded")
	}
	if response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q", Options: vectorstore.SearchOptions{TopK: MaxTopK + 1}}); response != nil || !errors.Is(err, vectorstore.ErrInvalidOptions) {
		t.Fatalf("TopK: %v, %v", response, err)
	}
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q"})
	if err != nil || response == nil || len(response.Results) != 0 || len(native.queries) != 0 {
		t.Fatalf("empty namespace: %v, %v", response, err)
	}
	installDocuments(t, store, &document.Document{ID: "one", Text: "text"})
	native.query = func(*pineconesdk.QueryByVectorValuesRequest) (*pineconesdk.QueryVectorsResponse, error) {
		return &pineconesdk.QueryVectorsResponse{Namespace: native.Namespace()}, nil
	}
	if response, err = store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q"}); err != nil || response == nil || len(response.Results) != 0 {
		t.Fatalf("empty query: %v, %v", response, err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	native.list = func(*pineconesdk.ListVectorsRequest) (*pineconesdk.ListVectorsResponse, error) {
		return nil, context.Cause(canceled)
	}
	if response, err = store.Search(canceled, &vectorstore.SearchRequest{Query: "q"}); response != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v, %v", response, err)
	}
}
