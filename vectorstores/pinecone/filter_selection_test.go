package pinecone

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"testing"

	"github.com/pinecone-io/go-pinecone/v4/pinecone"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

type metadataQueryIndex struct {
	indexConnection
	points      map[string]*pinecone.Vector
	listed      int
	deleted     []string
	repeatToken bool
	omitFetch   bool
}

func (m *metadataQueryIndex) ListVectors(_ context.Context, request *pinecone.ListVectorsRequest) (*pinecone.ListVectorsResponse, error) {
	m.listed++
	var ids []string
	for id := range m.points {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	start := 0
	if request.PaginationToken != nil {
		var err error
		start, err = strconv.Atoi(*request.PaginationToken)
		if err != nil {
			return nil, err
		}
	}
	end := min(start+2, len(ids))
	response := &pinecone.ListVectorsResponse{}
	for _, id := range ids[start:end] {
		response.VectorIds = append(response.VectorIds, new(id))
	}
	if end < len(ids) {
		response.NextPaginationToken = new(strconv.Itoa(end))
	}
	if m.repeatToken {
		response.NextPaginationToken = new("0")
	}
	return response, nil
}

func (m *metadataQueryIndex) FetchVectors(_ context.Context, ids []string) (*pinecone.FetchVectorsResponse, error) {
	response := &pinecone.FetchVectorsResponse{Vectors: map[string]*pinecone.Vector{}}
	for _, id := range ids {
		if !m.omitFetch {
			response.Vectors[id] = m.points[id]
		}
	}
	return response, nil
}

func (m *metadataQueryIndex) DeleteVectorsById(_ context.Context, ids []string) error {
	m.deleted = append(m.deleted, ids...)
	for _, id := range ids {
		delete(m.points, id)
	}
	return nil
}

func newMetadataQueryIndex(t *testing.T) *metadataQueryIndex {
	t.Helper()
	index := &metadataQueryIndex{points: map[string]*pinecone.Vector{}}
	for _, sample := range []struct {
		id     string
		value  any
		vector []float32
	}{
		{"a-scalar", "a", []float32{1, 0}},
		{"b-array", []any{"a"}, []float32{0.8, 0.6}},
		{"c-other", "b", []float32{1, 0}},
		{"d-array", []any{"a", "b"}, []float32{1, 0}},
		{"e-empty", []any{}, []float32{1, 0}},
	} {
		values, err := structpb.NewStruct(map[string]any{payloadDocumentContentKey: "document", "value": sample.value})
		if err != nil {
			t.Fatal(err)
		}
		index.points[sample.id] = &pinecone.Vector{Id: sample.id, Values: new(sample.vector), Metadata: values}
	}
	return index
}

func TestFilteredSearchAndDeletionDistinguishScalarsAndCollections(t *testing.T) {
	for _, sample := range []struct {
		source string
		want   []string
	}{
		{`value == 'a'`, []string{"a-scalar"}},
		{`value in ('a','b')`, []string{"a-scalar", "c-other"}},
		{`value has 'a'`, []string{"b-array", "d-array"}},
		{`value is null`, nil},
	} {
		t.Run(sample.source, func(t *testing.T) {
			predicate, err := filter.Parse(sample.source)
			if err != nil {
				t.Fatal(err)
			}
			index := newMetadataQueryIndex(t)
			store := upsertStore(t, index)
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 10, Filter: predicate}})
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, result := range response.Results {
				ids = append(ids, result.Document.ID)
			}
			slices.Sort(ids)
			if !slices.Equal(ids, sample.want) || index.listed != 3 {
				t.Fatalf("selected %v in %d pages; want %v", ids, index.listed, sample.want)
			}
			if err := store.DeleteWhere(t.Context(), predicate); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(index.deleted, sample.want) {
				t.Fatalf("deleted %v; want %v", index.deleted, sample.want)
			}
		})
	}
}

func TestFilteredSearchRanksEveryPageBeforeTopK(t *testing.T) {
	index := newMetadataQueryIndex(t)
	response, err := upsertStore(t, index).Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1, Filter: filter.Has("value", "a")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Document.ID != "d-array" || response.Results[0].Score != 1 {
		t.Fatalf("result = %#v", response.Results)
	}
	if index.listed != 3 {
		t.Fatalf("read %d pages, want 3", index.listed)
	}
}

func TestIncompleteSelectionNeverDeletes(t *testing.T) {
	for _, failure := range []string{"repeated cursor", "missing fetch"} {
		t.Run(failure, func(t *testing.T) {
			index := newMetadataQueryIndex(t)
			index.repeatToken = failure == "repeated cursor"
			index.omitFetch = failure == "missing fetch"
			if err := upsertStore(t, index).DeleteWhere(t.Context(), filter.EQ("value", "a")); err == nil {
				t.Fatal("incomplete selection succeeded")
			}
			if len(index.deleted) > 0 {
				t.Fatalf("deleted before completing selection: %v", index.deleted)
			}
		})
	}
}

func TestFetchedVectorsUseTheDocumentedMetric(t *testing.T) {
	for _, sample := range []struct {
		metric      DistanceMetric
		left, right []float32
		want        float64
	}{
		{DistanceCosine, []float32{1, 0}, []float32{3, 4}, 0.8},
		{DistanceDot, []float32{1, 0}, []float32{1, 0}, 0.7310585786300049},
		{DistanceEuclidean, []float32{1, 0}, []float32{3, 0}, 0.2},
	} {
		t.Run(string(sample.metric), func(t *testing.T) {
			raw, err := sample.metric.vectorMetric(sample.left, sample.right)
			score := sample.metric.score(raw)
			if err != nil || math.Abs(float64(score)-sample.want) > 1e-12 {
				t.Fatalf("score=%v, error=%v; want %v", score, err, sample.want)
			}
		})
	}
}

func TestMetadataRejectsShapesPineconeCannotStore(t *testing.T) {
	for _, value := range []any{nil, map[string]any{"nested": "a"}, []any{true}, []any{1.0}} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			if _, err := payloadValues(map[string]any{"value": value}); !errors.Is(err, errors.ErrUnsupported) {
				t.Fatalf("value %v: %v", value, err)
			}
		})
	}
}

func TestFilteredSearchRanksBeforeScoreNormalization(t *testing.T) {
	index := &metadataQueryIndex{points: map[string]*pinecone.Vector{}}
	for id, product := range map[string]float32{"a": 40, "z": 50} {
		values, err := structpb.NewStruct(map[string]any{payloadDocumentContentKey: "document", "value": "a"})
		if err != nil {
			t.Fatal(err)
		}
		index.points[id] = &pinecone.Vector{Id: id, Values: new([]float32{product, 0}), Metadata: values}
	}
	store := upsertStore(t, index)
	store.distanceMetric = DistanceDot
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1, Filter: filter.EQ("value", "a")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Document.ID != "z" {
		t.Fatalf("ranking lost raw metric: %#v", response)
	}
}
