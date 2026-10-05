package chroma

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"testing"

	v2 "github.com/amikos-tech/chroma-go/pkg/api/v2"
	ce "github.com/amikos-tech/chroma-go/pkg/embeddings"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

type boundaryCollection struct {
	schema                                 *v2.Schema
	rows                                   *v2.GetResultImpl
	query                                  *v2.QueryResultImpl
	getErr, queryErr, upsertErr, deleteErr error
	gets, queries, upserts, deletes        int
	queryOp                                *v2.CollectionQueryOp
	deleteOp                               *v2.CollectionDeleteOp
	addOp                                  *v2.CollectionAddOp
}

func (b *boundaryCollection) Schema() *v2.Schema { return b.schema }
func (b *boundaryCollection) Get(_ context.Context, options ...v2.GetOption) (v2.GetResult, error) {
	b.gets++
	if b.getErr != nil {
		return nil, b.getErr
	}
	op, err := v2.NewCollectionGetOp(options...)
	if err != nil {
		return nil, err
	}
	if op.Offset > 0 {
		return &v2.GetResultImpl{}, nil
	}
	return b.rows, nil
}
func (b *boundaryCollection) Query(_ context.Context, options ...v2.QueryOption) (v2.QueryResult, error) {
	b.queries++
	op, err := v2.NewCollectionQueryOp(options...)
	if err != nil {
		return nil, err
	}
	b.queryOp = op
	return b.query, b.queryErr
}
func (b *boundaryCollection) Upsert(_ context.Context, options ...v2.AddOption) error {
	b.upserts++
	op, err := v2.NewCollectionAddOp(options...)
	if err != nil {
		return err
	}
	b.addOp = op
	return b.upsertErr
}
func (b *boundaryCollection) Delete(_ context.Context, options ...v2.DeleteOption) error {
	b.deletes++
	op, err := v2.NewCollectionDeleteOp(options...)
	if err != nil {
		return err
	}
	b.deleteOp = op
	return b.deleteErr
}

type oneBatcher struct{}

func (oneBatcher) Batch(_ context.Context, docs []*document.Document) ([][]*document.Document, error) {
	return [][]*document.Document{docs}, nil
}

func boundaryConfig(t *testing.T, space v2.Space) (StoreConfig, *boundaryCollection, *int) {
	t.Helper()
	schema, err := v2.NewSchema(v2.WithVectorIndex(v2.EmbeddingKey, v2.NewVectorIndexConfig(v2.WithSpace(space))))
	if err != nil {
		t.Fatal(err)
	}
	b := &boundaryCollection{schema: schema, rows: &v2.GetResultImpl{}}
	calls := new(int)
	model := embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
		*calls++
		outputs := make([]*embedding.Output, len(request.Texts))
		for i := range outputs {
			outputs[i] = &embedding.Output{Embedding: []float64{1, 0}}
		}
		return embedding.NewResponse(outputs, nil)
	})
	return StoreConfig{Collection: b, EmbeddingModel: model, DocumentBatcher: oneBatcher{}}, b, calls
}
func oneRecord(raw string) *v2.GetResultImpl {
	return &v2.GetResultImpl{Ids: v2.DocumentIDs{"id"}, Documents: v2.Documents{v2.NewTextDocument("text")}, Metadatas: v2.DocumentMetadatas{v2.NewDocumentMetadata(v2.NewStringAttribute(metadataField, raw))}, Embeddings: ce.Embeddings{ce.NewEmbeddingFromFloat32([]float32{1, 0})}}
}
func queryRecords(rows *v2.GetResultImpl, distances ...ce.Distance) *v2.QueryResultImpl {
	return &v2.QueryResultImpl{IDLists: []v2.DocumentIDs{slices.Clone(rows.Ids)}, DocumentsLists: []v2.Documents{slices.Clone(rows.Documents)}, MetadatasLists: []v2.DocumentMetadatas{slices.Clone(rows.Metadatas)}, EmbeddingsLists: []ce.Embeddings{slices.Clone(rows.Embeddings)}, DistancesLists: []ce.Distances{distances}}
}
func parseBoundaryPredicate(t *testing.T, source string) filter.Predicate {
	t.Helper()
	predicate, err := filter.Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	return predicate
}

func TestPublicConstructionOwnsNoNativeLifecycle(t *testing.T) {
	for _, space := range []v2.Space{v2.SpaceCosine, v2.SpaceL2, v2.SpaceIP} {
		config, b, calls := boundaryConfig(t, space)
		store, err := NewStore(t.Context(), config)
		if err != nil {
			t.Fatal(err)
		}
		if _, owns := any(store).(vectorstore.Closer); owns {
			t.Fatal("store owns host collection")
		}
		if _, unsupported := any(store).(vectorstore.FilterDeleter); unsupported {
			t.Fatal("store advertises non-atomic filter deletion")
		}
		if *calls != 0 || b.gets != 1 {
			t.Fatalf("model=%d native reads=%d", *calls, b.gets)
		}
	}
	config, b, _ := boundaryConfig(t, v2.SpaceCosine)
	var typedNil *boundaryCollection
	for _, invalid := range []StoreConfig{{}, {Collection: typedNil}, {Collection: b}, {Collection: b, EmbeddingModel: config.EmbeddingModel}} {
		if store, err := NewStore(t.Context(), invalid); store != nil || err == nil {
			t.Fatalf("invalid store=%v err=%v", store, err)
		}
	}
	b.schema = nil
	if _, err := NewStore(t.Context(), config); err == nil {
		t.Fatal("accepted missing native schema")
	}
	config, _, _ = boundaryConfig(t, "unknown")
	if _, err := NewStore(t.Context(), config); err == nil {
		t.Fatal("accepted unknown native space")
	}
	config, b, _ = boundaryConfig(t, v2.SpaceL2)
	b.rows = oneRecord(`null`)
	b.rows.Metadatas[0] = v2.NewDocumentMetadata(v2.NewStringAttribute("old", "value"))
	if _, err := NewStore(t.Context(), config); err == nil {
		t.Fatal("accepted old metadata schema")
	}
}

func TestCoreJSONRoundtripAndStrictRecords(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `{"nested":{"array":[null,1e1000]},"precise":1.00000000000000001,"value":9007199254740993}`} {
		rows := oneRecord(raw)
		doc, err := decodeDocument(rows.Ids[0], rows.Documents[0], rows.Metadatas[0], rows.Embeddings[0])
		if err != nil {
			t.Fatal(err)
		}
		var want metadata.Map
		if err = want.UnmarshalJSON([]byte(raw)); err != nil {
			t.Fatal(err)
		}
		if !want.Equal(doc.Metadata) || (want == nil) != (doc.Metadata == nil) {
			t.Fatalf("doc=%v", doc)
		}
	}
	for _, raw := range []string{"", `[]`, `{"a":1,"a":2}`} {
		rows := oneRecord(raw)
		if _, err := decodeDocument(rows.Ids[0], rows.Documents[0], rows.Metadatas[0], rows.Embeddings[0]); err == nil {
			t.Fatal("accepted invalid", raw)
		}
	}
	for _, change := range []func(*v2.GetResultImpl){
		func(r *v2.GetResultImpl) { r.Documents[0] = nil }, func(r *v2.GetResultImpl) { r.Metadatas[0] = nil }, func(r *v2.GetResultImpl) { r.Embeddings[0] = nil },
		func(r *v2.GetResultImpl) { r.Ids[0] = " " }, func(r *v2.GetResultImpl) { r.Documents[0] = v2.NewTextDocument("") }, func(r *v2.GetResultImpl) {
			r.Embeddings[0] = ce.NewEmbeddingFromFloat32([]float32{float32(math.Inf(1))})
		},
	} {
		rows := oneRecord(`null`)
		change(rows)
		if _, err := decodeDocument(rows.Ids[0], rows.Documents[0], rows.Metadatas[0], rows.Embeddings[0]); err == nil {
			t.Fatal("accepted malformed row")
		}
	}
}

func TestPublicSearchUsesCoreSelection(t *testing.T) {
	config, b, calls := boundaryConfig(t, v2.SpaceCosine)
	store, err := NewStore(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	b.rows = oneRecord(`{"value":9007199254740993}`)
	b.query = queryRecords(b.rows, 2)
	predicate := parseBoundaryPredicate(t, "value == 9007199254740993")
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Filter: predicate}})
	if err != nil || response.First() == nil || response.First().Score != 0 {
		t.Fatalf("response=%v err=%v", response, err)
	}
	if *calls != 1 || !slices.Equal(b.queryOp.Ids, v2.DocumentIDs{"id"}) || b.queryOp.NResults != vectorstore.DefaultTopK || b.queryOp.Where != nil || len(b.queryOp.QueryTexts) != 0 {
		t.Fatalf("query=%+v model=%d", b.queryOp, *calls)
	}
	before := *calls
	queries := b.queries
	response, err = store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Filter: parseBoundaryPredicate(t, "value == 2")}})
	if err != nil || len(response.Results) != 0 || *calls != before || b.queries != queries {
		t.Fatalf("empty response=%v err=%v", response, err)
	}

}

func TestFailuresProduceNoResponseOrDeletion(t *testing.T) {
	config, b, calls := boundaryConfig(t, v2.SpaceIP)
	store, err := NewStore(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	b.rows = oneRecord(`{"value":"wrong"}`)
	predicate := parseBoundaryPredicate(t, "value < 2")
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1, MinScore: 1, Filter: predicate}})
	if response != nil || err == nil || *calls != 0 {
		t.Fatalf("response=%v err=%v model=%d", response, err, *calls)
	}
	b.rows = oneRecord(`null`)
	for _, change := range []func(*v2.QueryResultImpl){
		func(r *v2.QueryResultImpl) { r.IDLists = nil }, func(r *v2.QueryResultImpl) { r.DocumentsLists[0] = nil }, func(r *v2.QueryResultImpl) { r.IDLists[0][0] = "unexpected" },
		func(r *v2.QueryResultImpl) { r.DistancesLists[0][0] = ce.Distance(math.Inf(1)) }, func(r *v2.QueryResultImpl) {
			r.MetadatasLists[0][0] = v2.NewDocumentMetadata(v2.NewStringAttribute(metadataField, "bad"))
		},
	} {
		b.rows = oneRecord(`null`)
		b.query = queryRecords(b.rows, 1)
		change(b.query)
		response, err = store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{MinScore: 1}})
		if response != nil || err == nil {
			t.Fatalf("invalid response=%v err=%v", response, err)
		}
	}
	b.rows = oneRecord(`null`)
	b.query = queryRecords(b.rows, 1)
	failure := errors.New("transport failed")
	b.queryErr = failure
	response, err = store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query"})
	if response != nil || !errors.Is(err, failure) {
		t.Fatal(response, err)
	}
	b.getErr = failure
	response, err = store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query"})
	if response != nil || !errors.Is(err, failure) {
		t.Fatal(response, err)
	}
}

func TestIndexPublishesOnlyCoreJSONAndNativeVectors(t *testing.T) {
	config, b, _ := boundaryConfig(t, v2.SpaceL2)
	store, err := NewStore(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	facts := metadata.Map{"value": json.RawMessage(`1e1000`), "null": json.RawMessage(`null`)}
	if err = store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "original/路径", Text: "text", Metadata: facts}}}); err != nil {
		t.Fatal(err)
	}
	if b.upserts != 1 || !slices.Equal(b.addOp.Ids, v2.DocumentIDs{"original/路径"}) || !slices.Equal(b.addOp.Embeddings[0].(ce.Embedding).ContentAsFloat32(), []float32{1, 0}) {
		t.Fatal(b.addOp)
	}
	raw, ok := b.addOp.Metadatas[0].GetString(metadataField)
	if !ok {
		t.Fatal(b.addOp)
	}
	var restored metadata.Map
	if err = restored.UnmarshalJSON([]byte(raw)); err != nil || !restored.Equal(facts) {
		t.Fatal(raw, err)
	}
	failure := errors.New("write failed")
	b.upsertErr = failure
	if err = store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "next", Text: "text"}}}); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	config.EmbeddingModel = embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
		return embedding.NewResponse([]*embedding.Output{{Embedding: []float64{math.MaxFloat64, 0}}}, nil)
	})
	store, err = NewStore(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	before := b.upserts
	if err = store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "bad", Text: "text"}}}); err == nil || b.upserts != before {
		t.Fatal("published narrowing overflow")
	}
}

func TestDeleteIDsAndNativeDistanceProjection(t *testing.T) {
	config, b, _ := boundaryConfig(t, v2.SpaceIP)
	store, err := NewStore(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.DeleteIDs(t.Context(), nil); err != nil || b.deletes != 0 {
		t.Fatal(err)
	}
	if err = store.DeleteIDs(t.Context(), []string{"original/路径", "original/路径"}); err != nil || !slices.Equal(b.deleteOp.Ids, v2.DocumentIDs{"original/路径"}) || b.deleteOp.Where != nil {
		t.Fatal(b.deleteOp, err)
	}
	failure := errors.New("delete failed")
	b.deleteErr = failure
	if err = store.DeleteIDs(t.Context(), []string{"id"}); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	for _, test := range []struct {
		space    v2.Space
		distance float64
		want     vectorstore.Score
	}{{v2.SpaceCosine, 2, 0}, {v2.SpaceL2, 1, .5}, {v2.SpaceIP, 1, .5}} {
		score, scoreErr := distanceScore(test.space, test.distance)
		if scoreErr != nil || score != test.want {
			t.Fatal(score, scoreErr)
		}
	}
	if _, err = distanceScore("bad", 1); err == nil {
		t.Fatal("accepted invalid distance space")
	}
}
