package pinecone

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"slices"
	"strconv"
	"testing"

	pineconesdk "github.com/pinecone-io/go-pinecone/v4/pinecone"
	"google.golang.org/protobuf/proto"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/vectorstore"
)

type nativeFixture struct {
	points              map[string]*pineconesdk.Vector
	scores              map[string]float32
	metric              pineconesdk.IndexMetric
	upserts, deletes    [][]string
	queries             []*pineconesdk.QueryByVectorValuesRequest
	beforeDelete        func()
	list                func(*pineconesdk.ListVectorsRequest) (*pineconesdk.ListVectorsResponse, error)
	fetch               func([]string) (*pineconesdk.FetchVectorsResponse, error)
	query               func(*pineconesdk.QueryByVectorValuesRequest) (*pineconesdk.QueryVectorsResponse, error)
	upsert              func([]*pineconesdk.Vector) (uint32, error)
	deleteErr, closeErr error
	closed              bool
}

func (n *nativeFixture) Namespace() string { return "fixture" }

func (n *nativeFixture) Close() error {
	n.closed = true
	return n.closeErr
}

func (n *nativeFixture) UpsertVectors(_ context.Context, points []*pineconesdk.Vector) (uint32, error) {
	var ids []string
	for _, point := range points {
		ids = append(ids, point.Id)
	}
	n.upserts = append(n.upserts, ids)
	if n.upsert != nil {
		return n.upsert(points)
	}
	for _, point := range points {
		n.points[point.Id] = clonePoint(point)
	}
	return uint32(len(points)), nil
}

func (n *nativeFixture) ListVectors(_ context.Context, request *pineconesdk.ListVectorsRequest) (*pineconesdk.ListVectorsResponse, error) {
	if n.list != nil {
		return n.list(request)
	}
	ids := slices.Sorted(maps.Keys(n.points))
	start := 0
	if request.PaginationToken != nil {
		var err error
		start, err = strconv.Atoi(*request.PaginationToken)
		if err != nil {
			return nil, err
		}
	}
	end := min(start+2, len(ids))
	page := &pineconesdk.ListVectorsResponse{Namespace: n.Namespace()}
	for _, id := range ids[start:end] {
		page.VectorIds = append(page.VectorIds, new(id))
	}
	if end < len(ids) {
		page.NextPaginationToken = new(strconv.Itoa(end))
	}
	return page, nil
}

func (n *nativeFixture) FetchVectors(_ context.Context, ids []string) (*pineconesdk.FetchVectorsResponse, error) {
	if n.fetch != nil {
		return n.fetch(ids)
	}
	page := &pineconesdk.FetchVectorsResponse{Namespace: n.Namespace(), Vectors: make(map[string]*pineconesdk.Vector)}
	for _, id := range ids {
		if point, ok := n.points[id]; ok {
			page.Vectors[id] = clonePoint(point)
		}
	}
	return page, nil
}

func (n *nativeFixture) QueryByVectorValues(_ context.Context, request *pineconesdk.QueryByVectorValuesRequest) (*pineconesdk.QueryVectorsResponse, error) {
	n.queries = append(n.queries, request)
	if n.query != nil {
		return n.query(request)
	}
	page := &pineconesdk.QueryVectorsResponse{Namespace: n.Namespace()}
	for _, point := range n.points {
		if rawMetadataSelected(point, request.MetadataFilter) {
			score, ok := n.scores[point.Id]
			if !ok {
				score = 1
			}
			page.Matches = append(page.Matches, &pineconesdk.ScoredVector{Vector: clonePoint(point), Score: score})
		}
	}
	slices.SortFunc(page.Matches, func(left, right *pineconesdk.ScoredVector) int {
		order := cmp.Compare(right.Score, left.Score)
		if n.metric == pineconesdk.Euclidean {
			order = -order
		}
		if order != 0 {
			return order
		}
		return cmp.Compare(left.Vector.Id, right.Vector.Id)
	})
	page.Matches = page.Matches[:min(len(page.Matches), int(request.TopK))]
	return page, nil
}

func (n *nativeFixture) DeleteVectorsById(_ context.Context, ids []string) error {
	n.deletes = append(n.deletes, slices.Clone(ids))
	if n.deleteErr != nil {
		return n.deleteErr
	}
	for _, id := range ids {
		delete(n.points, id)
	}
	return nil
}

func (n *nativeFixture) DeleteVectorsByFilter(_ context.Context, selection *pineconesdk.MetadataFilter) error {
	if n.beforeDelete != nil {
		n.beforeDelete()
	}
	if n.deleteErr != nil {
		return n.deleteErr
	}
	for id, point := range n.points {
		if rawMetadataSelected(point, selection) {
			delete(n.points, id)
		}
	}
	return nil
}

// The fixture implements only native string equality and scripted native scores.
// It never interprets Core predicates or computes vector similarity.
func rawMetadataSelected(point *pineconesdk.Vector, selection *pineconesdk.MetadataFilter) bool {
	if selection == nil {
		return true
	}
	value := point.Metadata.Fields[metadataField].GetStringValue()
	for _, candidate := range selection.Fields[metadataField].GetStructValue().Fields["$in"].GetListValue().Values {
		if candidate.GetStringValue() == value {
			return true
		}
	}
	return false
}

func clonePoint(point *pineconesdk.Vector) *pineconesdk.Vector {
	copy := *point
	if point.Metadata != nil {
		copy.Metadata = proto.Clone(point.Metadata).(*pineconesdk.Metadata)
	}
	if point.Values != nil {
		copy.Values = new(slices.Clone(*point.Values))
	}
	return &copy
}

type fixtureBatcher struct{ size int }

func (f fixtureBatcher) Batch(_ context.Context, docs []*document.Document) ([][]*document.Document, error) {
	size := f.size
	if size == 0 {
		size = max(len(docs), 1)
	}
	return slices.Collect(slices.Chunk(docs, size)), nil
}

func fixtureModel() embedding.Model {
	return embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
		outputs := make([]*embedding.Output, len(request.Texts))
		for i := range outputs {
			outputs[i] = &embedding.Output{Embedding: []float64{1, 0}}
		}
		return embedding.NewResponse(outputs, nil)
	})
}

func fixtureStore(t *testing.T) (*Store, *nativeFixture) {
	t.Helper()
	model, err := embeddingclient.New(fixtureModel())
	if err != nil {
		t.Fatal(err)
	}
	native := &nativeFixture{points: make(map[string]*pineconesdk.Vector), scores: make(map[string]float32), metric: pineconesdk.Cosine}
	return &Store{index: native, schema: nativeSchema{dimensions: 2, metric: native.metric}, embeddingClient: model, documentBatcher: fixtureBatcher{}}, native
}

func installDocuments(t *testing.T, store *Store, docs ...*document.Document) {
	t.Helper()
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs}); err != nil {
		t.Fatal(err)
	}
}

func resultIDs(response *vectorstore.SearchResponse) []string {
	var ids []string
	for _, hit := range response.Results {
		ids = append(ids, hit.Document.ID)
	}
	return ids
}

var errNativeFailure = errors.New("native operation failed")
