package pinecone

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"slices"
	"strconv"
	"testing"

	pineconesdk "github.com/pinecone-io/go-pinecone/v7/pinecone"
	"google.golang.org/protobuf/proto"

	"github.com/Tangerg/scope/core/document"
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
		if n.metric == pineconesdk.IndexMetricEuclidean {
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
	field := selection.Fields[termsField].GetStructValue()
	if field == nil {
		return false
	}
	for _, wanted := range field.Fields["$in"].GetListValue().GetValues() {
		for _, v := range point.Metadata.GetFields()[termsField].GetListValue().GetValues() {
			if wanted.GetStringValue() == v.GetStringValue() {
				return true
			}
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

const fixtureSpace vectorstore.SpaceID = "paired-v1"

func fixtureVector(_ string) []float64 { return []float64{1, 0} }
func fixtureBatch(docs []*document.Document) *vectorstore.Batch {
	b := &vectorstore.Batch{Space: fixtureSpace}
	for _, doc := range docs {
		b.Records = append(b.Records, vectorstore.Record{Document: doc, Values: []float64{1, 0}})
	}
	return b
}
func (n *nativeFixture) DescribeIndex(context.Context, string) (*pineconesdk.Index, error) {
	idx := declaredIndex()
	idx.Schema.Fields[vectorField].DenseVector.Metric = n.metric
	return idx, nil
}
func fixtureStore(t *testing.T) (*Store, *nativeFixture) {
	t.Helper()
	native := &nativeFixture{points: make(map[string]*pineconesdk.Vector), scores: make(map[string]float32), metric: pineconesdk.IndexMetricCosine}
	return &Store{index: native, schema: nativeSchema{dimensions: 2, metric: native.metric}, space: fixtureSpace, control: native, name: "documents", host: declaredIndex().Host}, native
}

func installDocuments(t *testing.T, store *Store, docs ...*document.Document) {
	t.Helper()
	if err := store.Upsert(t.Context(), fixtureBatch(docs)); err != nil {
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

func unsupportedCases() map[string]error {
	return map[string]error{"in_excludes_array": vectorstore.ErrUnsupportedFilter, "has_excludes_scalar": vectorstore.ErrUnsupportedFilter, "null_excludes_empty_values": vectorstore.ErrUnsupportedFilter, "like_literal_syntax": vectorstore.ErrUnsupportedFilter, "like_literal_wildcards": vectorstore.ErrUnsupportedFilter, "like_newline_rune": vectorstore.ErrUnsupportedFilter, "large_integer": vectorstore.ErrUnsupportedFilter, "equal_decimal": vectorstore.ErrUnsupportedFilter, "decimal_ordering": vectorstore.ErrUnsupportedFilter, "decimal_membership": vectorstore.ErrUnsupportedFilter, "decimal_collection": vectorstore.ErrUnsupportedFilter, "negated_ordering_null": vectorstore.ErrUnsupportedFilter, "equal_number": vectorstore.ErrUnsupportedFilter, "equal_bool": vectorstore.ErrUnsupportedFilter, "not_equal": vectorstore.ErrUnsupportedFilter, "less": vectorstore.ErrUnsupportedFilter, "less_equal": vectorstore.ErrUnsupportedFilter, "greater": vectorstore.ErrUnsupportedFilter, "greater_equal": vectorstore.ErrUnsupportedFilter, "and": vectorstore.ErrUnsupportedFilter, "or": vectorstore.ErrUnsupportedFilter, "not": vectorstore.ErrUnsupportedFilter, "nested_logical": vectorstore.ErrUnsupportedFilter, "in": vectorstore.ErrUnsupportedFilter, "has": vectorstore.ErrUnsupportedFilter, "like_case": vectorstore.ErrUnsupportedFilter, "like_whole_value": vectorstore.ErrUnsupportedFilter, "like_percent": vectorstore.ErrUnsupportedFilter, "like_unicode_rune": vectorstore.ErrUnsupportedFilter, "is_null": vectorstore.ErrUnsupportedFilter, "is_not_null": vectorstore.ErrUnsupportedFilter, "missing_inequality": vectorstore.ErrUnsupportedFilter, "nested_key": vectorstore.ErrUnsupportedFilter, "array_index": vectorstore.ErrUnsupportedFilter, "negated_array_index": vectorstore.ErrUnsupportedFilter, "array_index_null": vectorstore.ErrUnsupportedFilter, "array_index_key": vectorstore.ErrUnsupportedFilter, "digit_key_excludes_array": vectorstore.ErrUnsupportedFilter, "no_matches": vectorstore.ErrUnsupportedFilter}
}
