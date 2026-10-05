package chroma

import (
	"cmp"
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"

	v2 "github.com/amikos-tech/chroma-go/pkg/api/v2"
	chromaEmbed "github.com/amikos-tech/chroma-go/pkg/embeddings"
	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

const Provider = "Chroma"

const (
	metadataField = "scope_metadata"
	pageSize      = 512
)

// Collection supplies native operations and the actual embedding schema. The
// host owns collection creation, index configuration and the SDK's lifecycle.
type Collection interface {
	Schema() *v2.Schema
	Upsert(context.Context, ...v2.AddOption) error
	Get(context.Context, ...v2.GetOption) (v2.GetResult, error)
	Query(context.Context, ...v2.QueryOption) (v2.QueryResult, error)
	Delete(context.Context, ...v2.DeleteOption) error
}

type StoreConfig struct {
	Collection      Collection
	EmbeddingModel  embedding.Model
	DocumentBatcher vectorstore.Batcher
}

func (s StoreConfig) Validate() error {
	if lo.IsNil(s.Collection) {
		return errors.New("chroma: Collection is required")
	}
	if lo.IsNil(s.EmbeddingModel) {
		return errors.New("chroma: EmbeddingModel is required")
	}
	if lo.IsNil(s.DocumentBatcher) {
		return errors.New("chroma: DocumentBatcher is required")
	}
	return nil
}

var (
	_ vectorstore.Indexer   = (*Store)(nil)
	_ vectorstore.Searcher  = (*Store)(nil)
	_ vectorstore.IDDeleter = (*Store)(nil)
)

// Store uses Core's metadata codec and predicate evaluator. Distance space is
// a read projection of the immutable native embedding index schema.
type Store struct {
	collection      Collection
	embeddingClient embeddingclient.Client
	documentBatcher vectorstore.Batcher
	space           v2.Space
}

func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	schema := config.Collection.Schema()
	if schema == nil {
		return nil, errors.New("chroma: native embedding schema is required")
	}
	key, exists := schema.GetKey(v2.EmbeddingKey)
	if !exists || key == nil || key.FloatList == nil || key.FloatList.VectorIndex == nil || !key.FloatList.VectorIndex.Enabled || key.FloatList.VectorIndex.Config == nil {
		return nil, errors.New("chroma: native embedding vector index is required")
	}
	space := key.FloatList.VectorIndex.Config.Space
	if space != v2.SpaceCosine && space != v2.SpaceL2 && space != v2.SpaceIP {
		return nil, fmt.Errorf("chroma: unsupported native distance space %q", space)
	}
	client, err := embeddingclient.New(config.EmbeddingModel)
	if err != nil {
		return nil, err
	}
	store := &Store{collection: config.Collection, embeddingClient: client, documentBatcher: config.DocumentBatcher, space: space}
	if _, err = store.selectIDs(ctx, nil); err != nil {
		return nil, fmt.Errorf("chroma: verify stored records: %w", err)
	}
	return store, nil
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
	var values metadata.Map
	if err = values.UnmarshalJSON([]byte(wire.JSON)); err != nil {
		return nil, fmt.Errorf("chroma: decode Core metadata for %q: %w", id, err)
	}
	doc := &document.Document{ID: string(id), Text: text.ContentString(), Metadata: values}
	if err = (&vectorstore.IndexRequest{Documents: []*document.Document{doc}}).Validate(); err != nil {
		return nil, err
	}
	if _, err = validateVector(vector.ContentAsFloat32()); err != nil {
		return nil, err
	}
	return doc, nil
}

func validateVector(vector []float32) (*embedding.Output, error) {
	values := make([]float64, len(vector))
	for i, value := range vector {
		values[i] = float64(value)
	}
	return embedding.NewOutput(values, nil)
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

func (s *Store) selectIDs(ctx context.Context, predicate filter.Predicate) ([]v2.DocumentID, error) {
	var candidates []v2.DocumentID
	seen := make(map[v2.DocumentID]struct{})
	for offset := 0; ; {
		result, err := s.collection.Get(ctx, v2.WithLimit(pageSize), v2.WithOffset(offset), v2.WithInclude(v2.IncludeDocuments, v2.IncludeMetadatas, v2.IncludeEmbeddings))
		if err != nil {
			return nil, nativeFailure(ctx, "scan records", err)
		}
		if lo.IsNil(result) {
			return nil, errors.New("chroma: scan returned no result")
		}
		ids, texts, facts, vectors := result.GetIDs(), result.GetDocuments(), result.GetMetadatas(), result.GetEmbeddings()
		if len(ids) > pageSize || len(texts) != len(ids) || len(facts) != len(ids) || len(vectors) != len(ids) {
			return nil, errors.New("chroma: scan returned inconsistent columns")
		}
		for i, id := range ids {
			if _, exists := seen[id]; exists {
				return nil, fmt.Errorf("chroma: scan repeated document %q", id)
			}
			seen[id] = struct{}{}
			doc, decodeErr := decodeDocument(id, texts[i], facts[i], vectors[i])
			if decodeErr != nil {
				return nil, decodeErr
			}
			match, matchErr := matches(doc, predicate)
			if matchErr != nil {
				return nil, fmt.Errorf("chroma: filter document %q: %w", id, matchErr)
			}
			if match {
				candidates = append(candidates, id)
			}
		}
		if len(ids) == 0 {
			return candidates, nil
		}
		offset += len(ids)
	}
}

func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	for i, doc := range request.Documents {
		if doc.Media != nil {
			return fmt.Errorf("chroma: %w: documents[%d] contains unsupported media", vectorstore.ErrInvalidDocument, i)
		}
	}
	batches, err := request.Batch(ctx, s.documentBatcher)
	if err != nil {
		return err
	}
	prepared := make([][]v2.AddOption, 0, len(batches))
	var allVectors []*embedding.Output
	for _, batch := range batches {
		texts, textErr := batch.Texts()
		if textErr != nil {
			return textErr
		}
		vectors, vectorErr := s.embeddingClient.EmbedTexts(ctx, texts)
		if vectorErr != nil {
			return vectorErr
		}
		ids := make([]v2.DocumentID, len(batch.Documents))
		nativeVectors := make([]chromaEmbed.Embedding, len(ids))
		facts := make([]v2.DocumentMetadata, len(ids))
		for i, doc := range batch.Documents {
			encoded, encodeErr := doc.Metadata.MarshalJSON()
			if encodeErr != nil {
				return encodeErr
			}
			vector := embedding.Float32Vector(vectors[i])
			output, validateErr := validateVector(vector)
			if validateErr != nil {
				return validateErr
			}
			allVectors = append(allVectors, output)
			ids[i] = v2.DocumentID(doc.ID)
			nativeVectors[i] = chromaEmbed.NewEmbeddingFromFloat32(vector)
			facts[i] = v2.NewDocumentMetadata(v2.NewStringAttribute(metadataField, string(encoded)))
		}
		prepared = append(prepared, []v2.AddOption{v2.WithIDs(ids...), v2.WithTexts(texts...), v2.WithEmbeddings(nativeVectors...), v2.WithMetadatas(facts...)})
	}
	if err = (&embedding.Response{Outputs: allVectors}).Validate(); err != nil {
		return err
	}
	for _, options := range prepared {
		if err = s.collection.Upsert(ctx, options...); err != nil {
			return nativeFailure(ctx, "upsert records", err)
		}
	}
	return nil
}

func distanceScore(space v2.Space, distance float64) (vectorstore.Score, error) {
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

func (s *Store) Search(ctx context.Context, request *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	if err = request.Validate(); err != nil {
		return nil, err
	}
	if err = request.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, err
	}
	defer func() {
		if err == nil {
			err = response.ValidateFor(request)
		}
		if err != nil {
			response = nil
		}
	}()
	candidates, err := s.selectIDs(ctx, request.Options.Filter)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return &vectorstore.SearchResponse{}, nil
	}
	vector, err := s.embeddingClient.EmbedText(ctx, request.Query)
	if err != nil {
		return nil, err
	}
	queryVector := embedding.Float32Vector(vector)
	if _, err = validateVector(queryVector); err != nil {
		return nil, err
	}
	result, queryErr := s.collection.Query(ctx, v2.WithIDs(candidates...), v2.WithQueryEmbeddings(chromaEmbed.NewEmbeddingFromFloat32(queryVector)), v2.WithNResults(request.Options.ResultLimit()), v2.WithInclude(v2.IncludeDocuments, v2.IncludeMetadatas, v2.IncludeEmbeddings, v2.IncludeDistances))
	if queryErr != nil {
		return nil, nativeFailure(ctx, "query candidates", queryErr)
	}
	ranked, decodeErr := s.decodeQuery(result, candidates, request.Options.ResultLimit(), request.Options.Filter)
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

func (s *Store) decodeQuery(result v2.QueryResult, expected []v2.DocumentID, limit int, predicate filter.Predicate) ([]rankedResult, error) {
	if lo.IsNil(result) {
		return nil, errors.New("chroma: query returned no result")
	}
	ids, texts, facts, vectors, distances := result.GetIDGroups(), result.GetDocumentsGroups(), result.GetMetadatasGroups(), result.GetEmbeddingsGroups(), result.GetDistancesGroups()
	if len(ids) != 1 || len(texts) != 1 || len(facts) != 1 || len(vectors) != 1 || len(distances) != 1 {
		return nil, errors.New("chroma: query must return one complete result group")
	}
	if len(ids[0]) > min(len(expected), limit) || len(texts[0]) != len(ids[0]) || len(facts[0]) != len(ids[0]) || len(vectors[0]) != len(ids[0]) || len(distances[0]) != len(ids[0]) {
		return nil, errors.New("chroma: query returned inconsistent columns or excess results")
	}
	seen := make(map[v2.DocumentID]struct{}, len(expected))
	var ranked []rankedResult
	for i, id := range ids[0] {
		if !slices.Contains(expected, id) {
			return nil, fmt.Errorf("chroma: query returned unexpected document %q", id)
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("chroma: query repeated document %q", id)
		}
		seen[id] = struct{}{}
		doc, err := decodeDocument(id, texts[0][i], facts[0][i], vectors[0][i])
		if err != nil {
			return nil, err
		}
		match, err := matches(doc, predicate)
		if err != nil {
			return nil, fmt.Errorf("chroma: filter query document %q: %w", id, err)
		}
		score, err := distanceScore(s.space, float64(distances[0][i]))
		if err != nil {
			return nil, err
		}
		if match {
			ranked = append(ranked, rankedResult{result: &vectorstore.SearchResult{Document: doc, Score: score}, distance: float64(distances[0][i])})
		}
	}
	return ranked, nil
}

func (s *Store) DeleteIDs(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	nativeIDs := make([]v2.DocumentID, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
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
