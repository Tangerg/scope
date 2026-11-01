package chroma

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"testing"
	"time"

	v2 "github.com/amikos-tech/chroma-go/pkg/api/v2"
	ce "github.com/amikos-tech/chroma-go/pkg/embeddings"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

type boundaryCollection struct {
	identity                               v2.CollectionMetadata
	dimension                              int
	read                                   func(context.Context, int, int) (v2.GetResult, error)
	schema                                 *v2.Schema
	rows                                   *v2.GetResultImpl
	query                                  *v2.QueryResultImpl
	getErr, queryErr, upsertErr, deleteErr error
	gets, queries, upserts, deletes        int
	queryOp                                *v2.CollectionQueryOp
	deleteOp                               *v2.CollectionDeleteOp
	addOp                                  *v2.CollectionAddOp
}

func (b *boundaryCollection) Metadata() v2.CollectionMetadata { return b.identity }
func (b *boundaryCollection) Dimension() int                  { return b.dimension }
func (b *boundaryCollection) Schema() *v2.Schema              { return b.schema }
func (b *boundaryCollection) Get(ctx context.Context, options ...v2.GetOption) (v2.GetResult, error) {
	b.gets++
	if b.getErr != nil {
		return nil, b.getErr
	}
	op, err := v2.NewCollectionGetOp(options...)
	if err != nil {
		return nil, err
	}
	if b.read != nil {
		return b.read(ctx, op.Offset, op.Limit)
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

const testSpace vectorstore.SpaceID = "test-encoder-v1"

func boundaryConfig(t *testing.T, metric v2.Space) (StoreConfig, *boundaryCollection) {
	t.Helper()
	schema, err := v2.NewSchema(v2.WithVectorIndex(v2.EmbeddingKey, v2.NewVectorIndexConfig(v2.WithSpace(metric))))
	if err != nil {
		t.Fatal(err)
	}
	b := &boundaryCollection{schema: schema, identity: v2.NewMetadata(v2.NewStringAttribute(SpaceMetadataKey, string(testSpace))), dimension: 2, rows: &v2.GetResultImpl{}}
	return StoreConfig{Collection: b, Space: testSpace}, b
}
func query(options vectorstore.SearchOptions) *vectorstore.Query {
	return &vectorstore.Query{Space: testSpace, Values: []float64{1, 0}, Options: options}
}
func record(doc *document.Document, coords ...float64) *vectorstore.Batch {
	return &vectorstore.Batch{Space: testSpace, Records: []vectorstore.Record{{Document: doc, Values: coords}}}
}
func budget() vectorstore.ScanBudget {
	return vectorstore.ScanBudget{MaxRecords: 16, MaxBytes: 4096, Timeout: time.Second}
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
	for _, metric := range []v2.Space{v2.SpaceCosine, v2.SpaceIP, v2.SpaceL2} {
		cfg, b := boundaryConfig(t, metric)
		store, err := NewStore(t.Context(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := any(store).(vectorstore.Closer); ok {
			t.Fatal("borrowed collection closed by adapter")
		}
		if _, ok := any(store).(vectorstore.FilterDeleter); ok {
			t.Fatal("non-atomic filter deletion exposed")
		}
		if b.gets+b.queries+b.upserts+b.deletes != 0 {
			t.Fatal("construction scanned or changed collection")
		}
	}
	_, b := boundaryConfig(t, v2.SpaceCosine)
	var missing *boundaryCollection
	for _, config := range []StoreConfig{{}, {Collection: missing, Space: testSpace}, {Collection: b}} {
		if got, err := NewStore(t.Context(), config); err == nil || got != nil {
			t.Fatalf("invalid config accepted: %+v", config)
		}
	}
	for _, change := range []func(*boundaryCollection){
		func(c *boundaryCollection) { c.schema = nil }, func(c *boundaryCollection) { c.dimension = 0 }, func(c *boundaryCollection) { c.identity = nil },
		func(c *boundaryCollection) {
			c.identity = v2.NewMetadata(v2.NewStringAttribute(SpaceMetadataKey, "another-space"))
		},
	} {
		cfg, collection := boundaryConfig(t, v2.SpaceCosine)
		change(collection)
		if _, err := NewStore(t.Context(), cfg); !errors.Is(err, vectorstore.ErrIncompatibleSpace) {
			t.Fatal(err)
		}
	}
	cfg, _ := boundaryConfig(t, "bad")
	if _, err := NewStore(t.Context(), cfg); !errors.Is(err, vectorstore.ErrIncompatibleSpace) {
		t.Fatal(err)
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

func TestQueryRejectsNonNativePredicateWithoutReadingSource(t *testing.T) {
	cfg, b := boundaryConfig(t, v2.SpaceCosine)
	s, err := NewStore(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got, queryErr := s.Query(t.Context(), query(vectorstore.SearchOptions{Filter: parseBoundaryPredicate(t, "value == 9007199254740993")})); got != nil || !errors.Is(queryErr, vectorstore.ErrUnsupportedFilter) {
		t.Fatal(got, queryErr)
	}
	if b.gets+b.queries != 0 {
		t.Fatal("unsupported filter triggered backend traffic")
	}
	b.rows = oneRecord(`{"value":9007199254740993}`)
	docs, err := s.Scan(t.Context(), ScanConfig{Budget: budget(), Filter: parseBoundaryPredicate(t, "value == 9007199254740993")})
	if err != nil || len(docs) != 1 || docs[0].ID != "id" {
		t.Fatal(docs, err)
	}
	if b.queries != 0 {
		t.Fatal("source scan was replaced with approximate ranking")
	}
}
func TestMalformedNativeResponseReturnsNoPartialResults(t *testing.T) {
	cfg, b := boundaryConfig(t, v2.SpaceCosine)
	s, err := NewStore(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*v2.QueryResultImpl){
		func(q *v2.QueryResultImpl) { q.IDLists = nil }, func(q *v2.QueryResultImpl) { q.DocumentsLists[0] = nil }, func(q *v2.QueryResultImpl) { q.IDLists[0] = append(q.IDLists[0], "id") }, func(q *v2.QueryResultImpl) {
			q.MetadatasLists[0][0] = v2.NewDocumentMetadata(v2.NewStringAttribute(metadataField, `[]`))
		},
		func(q *v2.QueryResultImpl) { q.EmbeddingsLists[0][0] = ce.NewEmbeddingFromFloat32([]float32{1}) }, func(q *v2.QueryResultImpl) { q.DistancesLists[0][0] = ce.Distance(math.Inf(1)) },
	} {
		b.query = queryRecords(oneRecord("null"), 2)
		mutate(b.query)
		if got, e := s.Query(t.Context(), query(vectorstore.SearchOptions{MinScore: 1})); got != nil || e == nil {
			t.Fatal("invalid low-score row accepted", got, e)
		}
	}
	failure := errors.New("query failed")
	b.queryErr = failure
	if got, e := s.Query(t.Context(), query(vectorstore.SearchOptions{})); got != nil || !errors.Is(e, failure) {
		t.Fatal(got, e)
	}
}
func TestUpsertPreflightsWholeBatchAndClassifiesUnknownRPC(t *testing.T) {
	cfg, b := boundaryConfig(t, v2.SpaceL2)
	s, err := NewStore(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	facts := metadata.Map{"value": json.RawMessage(`1e1000`), "null": json.RawMessage(`null`)}
	batch := record(&document.Document{ID: "original/路径", Text: "text", Metadata: facts}, 1, 0)
	if err = s.Upsert(t.Context(), batch); err != nil {
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
	for _, invalid := range [][]float64{{1}, {math.MaxFloat64, 0}, {math.NaN(), 0}} {
		bad := *batch
		bad.Records = append(slices.Clone(batch.Records), vectorstore.Record{Document: &document.Document{ID: "bad", Text: "bad"}, Values: invalid})
		if err = s.Upsert(t.Context(), &bad); err == nil || b.upserts != 1 {
			t.Fatal("published before validating all coordinates")
		}
	}
	b.upsertErr = errors.New("lost acknowledgment")
	err = s.Upsert(t.Context(), batch)
	failure, ok := errors.AsType[*vectorstore.WriteError](err)
	if !ok || !errors.Is(err, b.upsertErr) || !slices.Equal(failure.Unconfirmed, []string{"original/路径"}) || len(failure.Acknowledged)+len(failure.Rejected)+len(failure.Unattempted) != 0 {
		t.Fatal(err)
	}
	batch.Space = "other"
	if err = s.Upsert(t.Context(), batch); !errors.Is(err, vectorstore.ErrIncompatibleSpace) || b.upserts != 2 {
		t.Fatal(err)
	}
}
func TestBoundedScanRefusesPartialSubset(t *testing.T) {
	cfg, b := boundaryConfig(t, v2.SpaceCosine)
	s, err := NewStore(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	b.rows = oneRecord(`{"v":1}`)
	for _, limit := range []vectorstore.ScanBudget{{}, {MaxRecords: 1, MaxBytes: 1, Timeout: time.Second}} {
		out, e := s.Scan(t.Context(), ScanConfig{Budget: limit})
		if e == nil || out != nil {
			t.Fatal("invalid or exhausted budget gave rows")
		}
	}
	b.read = func(_ context.Context, offset, _ int) (v2.GetResult, error) {
		if offset >= 2 {
			return &v2.GetResultImpl{}, nil
		}
		out := oneRecord("null")
		out.Ids[0] = v2.DocumentID(string(rune('a' + offset)))
		return out, nil
	}
	limit := budget()
	limit.MaxRecords = 1
	out, e := s.Scan(t.Context(), ScanConfig{Budget: limit, Filter: parseBoundaryPredicate(t, "v == 9")})
	if !errors.Is(e, vectorstore.ErrScanBudget) || out != nil {
		t.Fatalf("nonmatching rejected scan: %v %v", out, e)
	}
	limit.MaxRecords = 2
	out, e = s.Scan(t.Context(), ScanConfig{Budget: limit})
	if e != nil || len(out) != 2 {
		t.Fatal(out, e)
	}
	b.read = func(_ context.Context, offset, _ int) (v2.GetResult, error) { return oneRecord("null"), nil }
	if out, e = s.Scan(t.Context(), ScanConfig{Budget: budget()}); e == nil || out != nil {
		t.Fatal("duplicate across pages accepted")
	}
	b.read = func(ctx context.Context, _, _ int) (v2.GetResult, error) { <-ctx.Done(); return nil, ctx.Err() }
	limit.Timeout = time.Millisecond
	if out, e = s.Scan(t.Context(), ScanConfig{Budget: limit}); !errors.Is(e, vectorstore.ErrScanBudget) || out != nil {
		t.Fatal(out, e)
	}
}
func TestSpaceAndDimensionGuardEveryDataPath(t *testing.T) {
	cfg, b := boundaryConfig(t, v2.SpaceCosine)
	s, err := NewStore(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	q := query(vectorstore.SearchOptions{})
	q.Values = []float64{1}
	if out, e := s.Query(t.Context(), q); !errors.Is(e, vectorstore.ErrIncompatibleSpace) || out != nil {
		t.Fatal(out, e)
	}
	q.Values = []float64{1, 0}
	q.Space = "different"
	if out, e := s.Query(t.Context(), q); !errors.Is(e, vectorstore.ErrIncompatibleSpace) || out != nil {
		t.Fatal(out, e)
	}
	b.dimension = 3
	if err = s.CheckDocuments(t.Context(), testSpace, []*document.Document{{ID: "a", Text: "x"}}); !errors.Is(err, vectorstore.ErrIncompatibleSpace) {
		t.Fatal(err)
	}
	if _, err = s.Scan(t.Context(), ScanConfig{Budget: budget()}); !errors.Is(err, vectorstore.ErrIncompatibleSpace) {
		t.Fatal(err)
	}
	if err = s.DeleteIDs(t.Context(), []string{"a"}); !errors.Is(err, vectorstore.ErrIncompatibleSpace) {
		t.Fatal(err)
	}
	if b.gets+b.queries+b.deletes+b.upserts != 0 {
		t.Fatal("incompatible request reached native data")
	}
}
func TestDeleteIDsAndNativeDistanceProjection(t *testing.T) {
	config, b := boundaryConfig(t, v2.SpaceIP)
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
