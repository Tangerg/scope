package chroma

import (
	"cmp"
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode/utf8"

	v2 "github.com/amikos-tech/chroma-go/pkg/api/v2"
	chromaEmbed "github.com/amikos-tech/chroma-go/pkg/embeddings"
	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

const Provider = "Chroma"

// SpaceMetadataKey identifies the Host-provisioned encoder pairing in the
// physical collection. The native dimension alone cannot establish this identity.
const SpaceMetadataKey = "scope.vector_space"

const (
	metadataField = "scope_metadata"
	pageSize      = 512
)

// Collection supplies native operations and the actual embedding schema. The
// host owns collection creation, index configuration and the SDK's lifecycle.
type Collection interface {
	Schema() *v2.Schema
	Metadata() v2.CollectionMetadata
	Dimension() int
	Upsert(context.Context, ...v2.AddOption) error
	Get(context.Context, ...v2.GetOption) (v2.GetResult, error)
	Query(context.Context, ...v2.QueryOption) (v2.QueryResult, error)
	Delete(context.Context, ...v2.DeleteOption) error
}

type StoreConfig struct {
	Collection Collection
	Space      vectorstore.SpaceID
}

func (s StoreConfig) Validate() error {
	if lo.IsNil(s.Collection) {
		return errors.New("chroma: Collection is required")
	}
	return s.Space.Validate()
}

var (
	_ vectorstore.Upserter  = (*Store)(nil)
	_ vectorstore.Querier   = (*Store)(nil)
	_ vectorstore.IDDeleter = (*Store)(nil)
)

// Store borrows a single collection handle, pinned to a native metric,
// dimension and Host-provisioned encoder space. It owns no encoder or transport.
type Store struct {
	collection Collection
	space      vectorstore.SpaceID
	metric     v2.Space
	dimensions int
}

func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	metric, dimensions, err := binding(config.Collection, config.Space)
	if err != nil {
		return nil, err
	}
	return &Store{collection: config.Collection, space: config.Space, metric: metric, dimensions: dimensions}, nil
}

func binding(collection Collection, space vectorstore.SpaceID) (v2.Space, int, error) {
	facts := collection.Metadata()
	if lo.IsNil(facts) {
		return "", 0, vectorstore.ErrIncompatibleSpace
	}
	marker, ok := facts.GetString(SpaceMetadataKey)
	if !ok || marker != string(space) {
		return "", 0, fmt.Errorf("chroma: %w: collection must declare %s=%q", vectorstore.ErrIncompatibleSpace, SpaceMetadataKey, space)
	}
	width := collection.Dimension()
	if width <= 0 {
		return "", 0, fmt.Errorf("chroma: %w: the native collection must already have a fixed dimension", vectorstore.ErrIncompatibleSpace)
	}
	schema := collection.Schema()
	if schema == nil {
		return "", 0, fmt.Errorf("chroma: %w: native embedding schema is required", vectorstore.ErrIncompatibleSpace)
	}
	key, ok := schema.GetKey(v2.EmbeddingKey)
	if !ok || key == nil || key.FloatList == nil || key.FloatList.VectorIndex == nil || !key.FloatList.VectorIndex.Enabled || key.FloatList.VectorIndex.Config == nil {
		return "", 0, fmt.Errorf("chroma: %w: native embedding vector index is required", vectorstore.ErrIncompatibleSpace)
	}
	metric := key.FloatList.VectorIndex.Config.Space
	if metric != v2.SpaceCosine && metric != v2.SpaceL2 && metric != v2.SpaceIP {
		return "", 0, fmt.Errorf("chroma: %w: unsupported native distance space %q", vectorstore.ErrIncompatibleSpace, metric)
	}
	return metric, width, nil
}

func (s *Store) verify(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	metric, width, err := binding(s.collection, s.space)
	if err != nil {
		return err
	}
	if metric != s.metric || width != s.dimensions {
		return fmt.Errorf("chroma: %w: collection schema changed", vectorstore.ErrIncompatibleSpace)
	}
	return nil
}

type metadataWire struct {
	JSON string `json:"scope_metadata"`
}

type rankedResult struct {
	result   *vectorstore.SearchResult
	distance float64
}

func decodeDocument(id v2.DocumentID, text v2.Document, facts v2.DocumentMetadata, vector chromaEmbed.Embedding) (*document.Document, error) {
	if lo.IsNil(text) || lo.IsNil(facts) || lo.IsNil(vector) {
		return nil, fmt.Errorf("chroma: incomplete stored record %q", id)
	}
	encoded, err := jsonv2.Marshal(facts)
	if err != nil {
		return nil, err
	}
	var wire metadataWire
	if err = jsonv2.Unmarshal(encoded, &wire, jsonv2.RejectUnknownMembers(true)); err != nil {
		return nil, fmt.Errorf("chroma: decode native metadata for %q: %w", id, err)
	}
	doc := new(document.Document)
	if err = document.UnmarshalStoredMetadata([]byte(wire.JSON), doc); err != nil {
		return nil, fmt.Errorf("chroma: decode Core metadata for %q: %w", id, err)
	}
	doc.ID, doc.Text = string(id), text.ContentString()
	if err = vectorstore.ValidateDocument(doc); err != nil {
		return nil, err
	}
	if err = validateVector(vector.ContentAsFloat32()); err != nil {
		return nil, err
	}
	return doc, nil
}

func validateVector(values []float32) error {
	if len(values) == 0 {
		return fmt.Errorf("%w: missing vector", vectorstore.ErrInvalidRequest)
	}
	for i, v := range values {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return fmt.Errorf("%w: nonfinite coordinate %d", vectorstore.ErrInvalidRequest, i)
		}
	}
	return nil
}

func (s *Store) coordinates(values []float64) ([]float32, error) {
	if len(values) != s.dimensions {
		return nil, fmt.Errorf("chroma: %w: got %d coordinates, want %d", vectorstore.ErrIncompatibleSpace, len(values), s.dimensions)
	}
	native := make([]float32, len(values))
	for i, v := range values {
		native[i] = float32(v)
	}
	if err := validateVector(native); err != nil {
		return nil, err
	}
	return native, nil
}

func matches(doc *document.Document, predicate filter.Predicate) (bool, error) {
	if predicate == nil {
		return true, nil
	}
	values, err := doc.Metadata.Values()
	if err != nil {
		return false, err
	}
	return filter.Match(predicate, values)
}

func (s *Store) CheckDocuments(ctx context.Context, space vectorstore.SpaceID, docs []*document.Document) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := space.Require(s.space); err != nil {
		return err
	}
	if err := vectorstore.ValidateDocuments(docs); err != nil {
		return err
	}
	for i, doc := range docs {
		if doc.Media != nil {
			return fmt.Errorf("chroma: %w: documents[%d] contains unsupported media", vectorstore.ErrInvalidDocument, i)
		}
	}
	return s.verify(ctx)
}

// Upsert prepares every record before issuing one native RPC. A transport
// failure does not prove that any individual record was rejected.
func (s *Store) Upsert(ctx context.Context, batch *vectorstore.Batch) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := batch.ValidateFor(s.space); err != nil {
		return err
	}
	ids := make([]v2.DocumentID, len(batch.Records))
	texts := make([]string, len(ids))
	vectors := make([]chromaEmbed.Embedding, len(ids))
	facts := make([]v2.DocumentMetadata, len(ids))
	for i, record := range batch.Records {
		if err := ctx.Err(); err != nil {
			return err
		}
		if record.Document.Media != nil {
			return fmt.Errorf("chroma: %w: records[%d] contains unsupported media", vectorstore.ErrInvalidDocument, i)
		}
		raw, err := document.MarshalStoredMetadata(record.Document)
		if err != nil {
			return err
		}
		narrow, err := s.coordinates(record.Values)
		if err != nil {
			return err
		}
		ids[i] = v2.DocumentID(record.Document.ID)
		texts[i] = record.Document.Text
		vectors[i] = chromaEmbed.NewEmbeddingFromFloat32(narrow)
		facts[i] = v2.NewDocumentMetadata(v2.NewStringAttribute(metadataField, string(raw)))
	}
	if err := s.verify(ctx); err != nil {
		return err
	}
	if err := s.collection.Upsert(ctx, v2.WithIDs(ids...), v2.WithTexts(texts...), v2.WithEmbeddings(vectors...), v2.WithMetadatas(facts...)); err != nil {
		unknown := make([]string, len(ids))
		for i, id := range ids {
			unknown[i] = string(id)
		}
		return &vectorstore.WriteError{Unconfirmed: unknown, Err: nativeFailure(ctx, "upsert records", err)}
	}
	return nil
}

func (s *Store) CheckQuery(ctx context.Context, space vectorstore.SpaceID, options vectorstore.SearchOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := space.Require(s.space); err != nil {
		return err
	}
	if err := options.Validate(); err != nil {
		return err
	}
	if err := options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return err
	}
	if options.Filter != nil {
		return vectorstore.ErrUnsupportedFilter
	}
	return s.verify(ctx)
}

func distanceScore(space v2.Space, distance float64) (vectorstore.Score, error) {
	if math.IsNaN(distance) || math.IsInf(distance, 0) {
		return 0, fmt.Errorf("chroma: nonfinite distance")
	}
	var score vectorstore.Score
	switch space {
	case v2.SpaceCosine:
		score = vectorstore.ScoreFromCosineDistance(distance)
	case v2.SpaceL2:
		score = vectorstore.ScoreFromDistance(distance)
	case v2.SpaceIP:
		score = vectorstore.ScoreFromOneMinusInnerProductDistance(distance)
	default:
		return 0, fmt.Errorf("chroma: unsupported native distance space %q", space)
	}
	return score, score.Validate()
}

func (s *Store) Query(ctx context.Context, request *vectorstore.Query) (response *vectorstore.SearchResponse, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = request.ValidateFor(s.space); err != nil {
		return nil, err
	}
	if err = s.CheckQuery(ctx, request.Space, request.Options); err != nil {
		return nil, err
	}
	vector, err := s.coordinates(request.Values)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err == nil {
			err = request.ValidateResponse(response)
		}
		if err != nil {
			response = nil
		}
	}()
	result, queryErr := s.collection.Query(ctx, v2.WithQueryEmbeddings(chromaEmbed.NewEmbeddingFromFloat32(vector)), v2.WithNResults(request.Options.ResultLimit()), v2.WithInclude(v2.IncludeDocuments, v2.IncludeMetadatas, v2.IncludeEmbeddings, v2.IncludeDistances))
	if queryErr != nil {
		return nil, nativeFailure(ctx, "query", queryErr)
	}
	ranked, decodeErr := s.decodeQuery(result, request.Options.ResultLimit())
	if decodeErr != nil {
		return nil, decodeErr
	}
	slices.SortFunc(ranked, func(left, right rankedResult) int {
		if order := cmp.Compare(left.distance, right.distance); order != 0 {
			return order
		}
		return strings.Compare(left.result.Document.ID, right.result.Document.ID)
	})
	results := make([]*vectorstore.SearchResult, 0, min(len(ranked), request.Options.ResultLimit()))
	for _, row := range ranked {
		if row.result.Score >= request.Options.MinScore {
			results = append(results, row.result)
			if len(results) == request.Options.ResultLimit() {
				break
			}
		}
	}
	return &vectorstore.SearchResponse{Results: results}, nil
}

func (s *Store) decodeQuery(result v2.QueryResult, limit int) ([]rankedResult, error) {
	if lo.IsNil(result) {
		return nil, errors.New("chroma: query returned no result")
	}
	ids, texts, facts, vectors, distances := result.GetIDGroups(), result.GetDocumentsGroups(), result.GetMetadatasGroups(), result.GetEmbeddingsGroups(), result.GetDistancesGroups()
	if len(ids) != 1 || len(texts) != 1 || len(facts) != 1 || len(vectors) != 1 || len(distances) != 1 {
		return nil, errors.New("chroma: query must return one complete result group")
	}
	if len(ids[0]) > limit || len(texts[0]) != len(ids[0]) || len(facts[0]) != len(ids[0]) || len(vectors[0]) != len(ids[0]) || len(distances[0]) != len(ids[0]) {
		return nil, errors.New("chroma: query returned inconsistent columns or excess results")
	}
	seen := make(map[v2.DocumentID]struct{}, len(ids[0]))
	var ranked []rankedResult
	for i, id := range ids[0] {
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("chroma: query repeated document %q", id)
		}
		seen[id] = struct{}{}
		doc, err := decodeDocument(id, texts[0][i], facts[0][i], vectors[0][i])
		if err != nil {
			return nil, err
		}
		if len(vectors[0][i].ContentAsFloat32()) != s.dimensions {
			return nil, fmt.Errorf("chroma: %w: result dimension", vectorstore.ErrIncompatibleSpace)
		}
		score, err := distanceScore(s.metric, float64(distances[0][i]))
		if err != nil {
			return nil, err
		}
		ranked = append(ranked, rankedResult{result: &vectorstore.SearchResult{Document: doc, Score: score}, distance: float64(distances[0][i])})
	}
	return ranked, nil
}

func (s *Store) DeleteIDs(ctx context.Context, ids []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	if err := s.verify(ctx); err != nil {
		return err
	}
	nativeIDs := make([]v2.DocumentID, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if !utf8.ValidString(id) || strings.TrimSpace(id) == "" {
			return vectorstore.ErrInvalidDocument
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		nativeIDs = append(nativeIDs, v2.DocumentID(id))
	}
	if err := s.collection.Delete(ctx, v2.WithIDs(nativeIDs...)); err != nil {
		return nativeFailure(ctx, "delete IDs", err)
	}
	return nil
}

// The SDK's ChromaError keeps transport failures as text, discarding their
// error chain. Cancellation remains owned by the calling context.
func nativeFailure(ctx context.Context, operation string, err error) error {
	return fmt.Errorf("chroma: %s: %w", operation, errors.Join(err, context.Cause(ctx)))
}

// Scan returns the matching documents from observed pages, sorted by ID.
// Counted bytes are ID, document text, stored Core metadata JSON and four bytes
// per coordinate. The SDK has already materialized the current page: the byte
// budget is not a transport frame limit. Offset pagination is not a snapshot.
func (s *Store) Scan(ctx context.Context, config ScanConfig) ([]*document.Document, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if err := s.verify(ctx); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeoutCause(ctx, config.Budget.Timeout, vectorstore.ErrScanBudget)
	defer cancel()
	seen := make(map[v2.DocumentID]struct{})
	var used int64
	var found []*document.Document
	for offset := 0; ; {
		if err := ctx.Err(); err != nil {
			return nil, errors.Join(err, context.Cause(ctx))
		}
		limit := min(pageSize, config.Budget.MaxRecords-offset+1)
		page, err := s.collection.Get(ctx, v2.WithOffset(offset), v2.WithLimit(limit), v2.WithInclude(v2.IncludeDocuments, v2.IncludeMetadatas, v2.IncludeEmbeddings))
		if err != nil {
			return nil, nativeFailure(ctx, "scan records", err)
		}
		if err = ctx.Err(); err != nil {
			return nil, errors.Join(err, context.Cause(ctx))
		}
		if lo.IsNil(page) {
			return nil, errors.New("chroma: scan returned no result")
		}
		ids, texts, facts, vectors := page.GetIDs(), page.GetDocuments(), page.GetMetadatas(), page.GetEmbeddings()
		if len(ids) > limit || len(texts) != len(ids) || len(facts) != len(ids) || len(vectors) != len(ids) {
			return nil, errors.New("chroma: scan returned inconsistent columns")
		}
		if len(ids) > config.Budget.MaxRecords-offset {
			return nil, vectorstore.ErrScanBudget
		}
		for i, id := range ids {
			if err = ctx.Err(); err != nil {
				return nil, errors.Join(err, context.Cause(ctx))
			}
			if _, ok := seen[id]; ok {
				return nil, fmt.Errorf("chroma: scan repeated document %q", id)
			}
			seen[id] = struct{}{}
			if lo.IsNil(texts[i]) || lo.IsNil(facts[i]) || lo.IsNil(vectors[i]) {
				return nil, errors.New("chroma: incomplete scan record")
			}
			raw, ok := facts[i].GetString(metadataField)
			if !ok {
				return nil, errors.New("chroma: missing stored metadata")
			}
			width := len(vectors[i].ContentAsFloat32())
			if width != s.dimensions {
				return nil, fmt.Errorf("chroma: %w: scan dimension", vectorstore.ErrIncompatibleSpace)
			}
			for _, bytes := range []int64{int64(len(id)), int64(len(texts[i].ContentString())), int64(len(raw)), int64(width) * 4} {
				if bytes > config.Budget.MaxBytes-used {
					return nil, vectorstore.ErrScanBudget
				}
				used += bytes
			}
			doc, err := decodeDocument(id, texts[i], facts[i], vectors[i])
			if err != nil {
				return nil, err
			}
			match, err := matches(doc, config.Filter)
			if err != nil {
				return nil, fmt.Errorf("chroma: filter document %q: %w", id, err)
			}
			if match {
				found = append(found, doc)
			}
		}
		if len(ids) == 0 {
			break
		}
		offset += len(ids)
	}
	slices.SortFunc(found, func(a, b *document.Document) int { return strings.Compare(a.ID, b.ID) })
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, context.Cause(ctx))
	}
	return found, nil
}
