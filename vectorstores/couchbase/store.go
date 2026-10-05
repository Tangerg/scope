package couchbase

import (
	"cmp"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"unicode/utf8"

	"github.com/couchbase/gocb/v2"
	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

const Provider = "Couchbase"

const (
	DefaultScopeName          = "_default"
	DefaultCollectionName     = "_default"
	DefaultSimilarity         = SimilarityDotProduct
	rankingBatchSize          = 512
	maximumCollectionKeyBytes = 246
	maximumDefaultKeyBytes    = 250
)

// Similarity selects the native VECTOR_DISTANCE metric.
type Similarity string

const (
	SimilarityCosine     Similarity = "COSINE"
	SimilarityL2Norm     Similarity = "L2"
	SimilarityDotProduct Similarity = "DOT"
)

func (s Similarity) Valid() bool {
	return s == SimilarityCosine || s == SimilarityL2Norm || s == SimilarityDotProduct
}

func (s Similarity) String() string { return string(s) }

func (s Similarity) score(distance float64) vectorstore.Score {
	switch s {
	case SimilarityCosine:
		return vectorstore.ScoreFromCosineDistance(distance)
	case SimilarityL2Norm:
		return vectorstore.ScoreFromDistance(distance)
	default:
		return vectorstore.ScoreFromNegativeInnerProductDistance(distance)
	}
}

// StoreConfig wires a host-owned SDK cluster and an exclusively managed document
// collection. The host owns connecting and closing the cluster.
type StoreConfig struct {
	Cluster    *gocb.Cluster
	BucketName string
	// ScopeName and CollectionName default to the native default namespace.
	ScopeName       string
	CollectionName  string
	EmbeddingModel  embedding.Model
	DocumentBatcher vectorstore.Batcher
	// Similarity defaults to DefaultSimilarity.
	Similarity Similarity
	// InitializeSchema creates the collection's default primary query index.
	// The bucket, scope and collection must already exist.
	InitializeSchema bool
}

func (s StoreConfig) Validate() error {
	s.applyDefaults()
	if s.Cluster == nil {
		return errors.New("couchbase: Cluster is required")
	}
	if lo.IsNil(s.EmbeddingModel) {
		return errors.New("couchbase: EmbeddingModel is required")
	}
	if lo.IsNil(s.DocumentBatcher) {
		return errors.New("couchbase: DocumentBatcher is required")
	}
	if !s.Similarity.Valid() {
		return fmt.Errorf("couchbase: unsupported Similarity %q", s.Similarity)
	}
	for _, field := range []struct{ name, value string }{{"BucketName", s.BucketName}, {"ScopeName", s.ScopeName}, {"CollectionName", s.CollectionName}} {
		if err := identifier(field.value).validate(field.name); err != nil {
			return err
		}
	}
	return nil
}

func (s *StoreConfig) applyDefaults() {
	s.ScopeName = cmp.Or(s.ScopeName, DefaultScopeName)
	s.CollectionName = cmp.Or(s.CollectionName, DefaultCollectionName)
	s.Similarity = cmp.Or(s.Similarity, DefaultSimilarity)
}

var (
	_ vectorstore.Indexer       = (*Store)(nil)
	_ vectorstore.Searcher      = (*Store)(nil)
	_ vectorstore.FilterDeleter = (*Store)(nil)
	_ vectorstore.IDDeleter     = (*Store)(nil)
)

// Store performs exact semantic search over Core-selected documents.
type Store struct {
	scope           *gocb.Scope
	collection      *gocb.Collection
	keyspace        string
	embeddingClient embeddingclient.Client
	documentBatcher vectorstore.Batcher
	similarity      Similarity
	maximumKeyBytes int
}

func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	config.applyDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	client, err := embeddingclient.New(config.EmbeddingModel)
	if err != nil {
		return nil, fmt.Errorf("couchbase: create embedding client: %w", err)
	}
	scope := config.Cluster.Bucket(config.BucketName).Scope(config.ScopeName)
	store := &Store{
		scope: scope, collection: scope.Collection(config.CollectionName),
		keyspace:        fmt.Sprintf("`%s`.`%s`.`%s`", config.BucketName, config.ScopeName, config.CollectionName),
		embeddingClient: client, documentBatcher: config.DocumentBatcher, similarity: config.Similarity,
		maximumKeyBytes: maximumCollectionKeyBytes,
	}
	if config.ScopeName == DefaultScopeName && config.CollectionName == DefaultCollectionName {
		store.maximumKeyBytes = maximumDefaultKeyBytes
	}
	if config.InitializeSchema {
		if err := store.collection.QueryIndexes().CreatePrimaryIndex(&gocb.CreatePrimaryQueryIndexOptions{Context: ctx, IgnoreIfExists: true}); err != nil {
			return nil, fmt.Errorf("couchbase: create primary index: %w", errors.Join(err, ctx.Err()))
		}
	}
	// Probe the actual query capability without model I/O.
	probeRows := 0
	if err := store.runStatement(ctx, fmt.Sprintf(`SELECT VECTOR_DISTANCE(c.embedding, [1,0], %q) AS distance FROM [{"embedding":[1,0]}] AS c`, config.Similarity), nil, func(raw json.RawMessage) error {
		probeRows++
		var row distanceRow
		if err := jsonv2.Unmarshal(raw, &row, jsonv2.RejectUnknownMembers(true)); err != nil {
			return err
		}
		if row.Distance == nil {
			return errors.New("couchbase: native vector distance is missing")
		}
		return config.Similarity.score(*row.Distance).Validate()
	}); err != nil {
		return nil, fmt.Errorf("couchbase: require native vector distance: %w", err)
	}
	if probeRows != 1 {
		return nil, errors.New("couchbase: native vector distance probe is incomplete")
	}
	if err := store.runStatement(ctx, "SELECT RAW META(c).id FROM "+store.keyspace+" AS c LIMIT 1", nil, nil); err != nil {
		return nil, fmt.Errorf("couchbase: require queryable collection: %w", err)
	}
	return store, nil
}

func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) error {
	if err := request.Validate(); err != nil {
		return fmt.Errorf("couchbase.Store.Index: %w", err)
	}
	for i, doc := range request.Documents {
		if doc.Media != nil {
			return fmt.Errorf("couchbase.Store.Index: %w: documents[%d] contains unsupported media", vectorstore.ErrInvalidDocument, i)
		}
		if err := s.validateKey(doc.ID); err != nil {
			return err
		}
	}
	batches, err := request.Batch(ctx, s.documentBatcher)
	if err != nil {
		return fmt.Errorf("couchbase: batch documents: %w", err)
	}
	for _, batch := range batches {
		texts, err := batch.Texts()
		if err != nil {
			return err
		}
		vectors, err := s.embeddingClient.EmbedTexts(ctx, texts)
		if err != nil {
			return fmt.Errorf("couchbase: embed documents: %w", err)
		}
		payloads := make([][]byte, len(batch.Documents))
		for i, doc := range batch.Documents {
			payloads[i], err = encodeStoredDocument(texts[i], doc.Metadata, vectors[i])
			if err != nil {
				return fmt.Errorf("couchbase: encode %s: %w", doc.ID, err)
			}
		}
		for i, doc := range batch.Documents {
			if _, err := s.collection.Upsert(doc.ID, payloads[i], &gocb.UpsertOptions{Context: ctx, Transcoder: gocb.NewRawJSONTranscoder()}); err != nil {
				return fmt.Errorf("couchbase: upsert %s: %w", doc.ID, errors.Join(err, ctx.Err()))
			}
		}
	}
	return nil
}

func (s *Store) Search(ctx context.Context, request *vectorstore.SearchRequest) (*vectorstore.SearchResponse, error) {
	if err := request.Validate(); err != nil {
		return nil, fmt.Errorf("couchbase.Store.Search: %w", err)
	}
	if err := request.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, fmt.Errorf("couchbase.Store.Search: %w", err)
	}
	candidates, err := s.selectDocuments(ctx, request.Options.Filter)
	if err != nil {
		return nil, err
	}
	var ranked []rankedResult
	if len(candidates) != 0 {
		vector, err := s.embeddingClient.EmbedText(ctx, request.Query)
		if err != nil {
			return nil, fmt.Errorf("couchbase: embed query: %w", err)
		}
		for batch := range slices.Chunk(candidates, rankingBatchSize) {
			projections := make([]vectorProjection, len(batch))
			for i, candidate := range batch {
				projections[i] = vectorProjection{Position: i, Embedding: candidate.embedding}
			}
			seen := make([]bool, len(batch))
			err := s.runStatement(ctx, fmt.Sprintf("SELECT c.position, VECTOR_DISTANCE(c.embedding, $vector, %q) AS distance FROM $candidates AS c", s.similarity), map[string]any{"candidates": projections, "vector": vector}, func(raw json.RawMessage) error {
				var row distanceRow
				if err := jsonv2.Unmarshal(raw, &row, jsonv2.RejectUnknownMembers(true)); err != nil {
					return fmt.Errorf("couchbase: decode distance: %w", err)
				}
				if row.Position == nil || *row.Position < 0 || *row.Position >= len(batch) || seen[*row.Position] || row.Distance == nil {
					return errors.New("couchbase: invalid native distance result")
				}
				seen[*row.Position] = true
				hit, err := vectorstore.NewSearchResult(batch[*row.Position].document, s.similarity.score(*row.Distance))
				if err != nil {
					return err
				}
				if hit.Score >= request.Options.MinScore {
					ranked = append(ranked, rankedResult{result: hit, distance: *row.Distance})
				}
				return nil
			})
			if err != nil {
				return nil, err
			}
			if slices.Contains(seen, false) {
				return nil, errors.New("couchbase: native distance result is incomplete")
			}
			slices.SortFunc(ranked, compareResults)
			if len(ranked) > request.Options.ResultLimit() {
				ranked = ranked[:request.Options.ResultLimit()]
			}
		}
	}
	results := make([]*vectorstore.SearchResult, len(ranked))
	for i, hit := range ranked {
		results[i] = hit.result
	}
	response := &vectorstore.SearchResponse{Results: results}
	if err := response.ValidateFor(request); err != nil {
		return nil, err
	}
	return response, nil
}

// selectDocuments captures each complete KV record once. Filtering, ranking and
// returned content use that same projection even if embedding causes a write.
func (s *Store) selectDocuments(ctx context.Context, predicate filter.Predicate) ([]storedCandidate, error) {
	var candidates []storedCandidate
	err := s.runStatement(ctx, "SELECT META(c).id AS id, META(c).cas AS cas, c AS record FROM "+s.keyspace+" AS c", nil, func(raw json.RawMessage) error {
		candidate, err := decodeStoredRow(raw)
		if err != nil {
			return err
		}
		if predicate != nil {
			values, err := candidate.document.Metadata.Values()
			if err != nil {
				return err
			}
			match, err := filter.Match(predicate, values)
			if err != nil {
				return fmt.Errorf("couchbase: evaluate filter for %s: %w", candidate.document.ID, err)
			}
			if !match {
				return nil
			}
		}
		candidates = append(candidates, candidate)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return candidates, nil
}

func (s *Store) runStatement(ctx context.Context, stmt string, params map[string]any, row func(json.RawMessage) error) (err error) {
	if err = ctx.Err(); err != nil {
		return err
	}
	result, err := s.scope.Query(stmt, &gocb.QueryOptions{Context: ctx, NamedParameters: params, ScanConsistency: gocb.QueryScanConsistencyRequestPlus, UseReplica: gocb.QueryUseReplicaLevelOff, Readonly: true})
	if err != nil {
		return fmt.Errorf("couchbase: query: %w", errors.Join(err, ctx.Err()))
	}
	defer func() { err = errors.Join(err, result.Close()) }()
	for result.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		if row != nil {
			var raw json.RawMessage
			if err := result.Row(&raw); err != nil {
				return fmt.Errorf("couchbase: read row: %w", err)
			}
			if err := row(raw); err != nil {
				return err
			}
		}
	}
	if err := result.Err(); err != nil {
		return fmt.Errorf("couchbase: read result: %w", errors.Join(err, ctx.Err()))
	}
	return ctx.Err()
}

// DeleteWhere preflights the entire collection before the first mutation. CAS
// prevents deletion of a replacement that was never evaluated by Core.
func (s *Store) DeleteWhere(ctx context.Context, predicate filter.Predicate) error {
	if predicate == nil {
		return vectorstore.ErrMissingFilter
	}
	if err := predicate.Validate(); err != nil {
		return fmt.Errorf("couchbase.Store.DeleteWhere: %w", err)
	}
	candidates, err := s.selectDocuments(ctx, predicate)
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		if _, err := s.collection.Remove(candidate.document.ID, &gocb.RemoveOptions{Context: ctx, Cas: candidate.cas}); err != nil {
			if errors.Is(err, gocb.ErrDocumentNotFound) {
				continue
			}
			return fmt.Errorf("couchbase: remove evaluated version of %s: %w", candidate.document.ID, errors.Join(err, ctx.Err()))
		}
	}
	return nil
}

func (s *Store) DeleteIDs(ctx context.Context, ids []string) error {
	for _, id := range ids {
		if err := s.validateKey(id); err != nil {
			return err
		}
	}
	for _, id := range ids {
		if _, err := s.collection.Remove(id, &gocb.RemoveOptions{Context: ctx}); err != nil {
			if errors.Is(err, gocb.ErrDocumentNotFound) {
				continue
			}
			return fmt.Errorf("couchbase: remove %s: %w", id, errors.Join(err, ctx.Err()))
		}
	}
	return nil
}

func compareResults(left, right rankedResult) int {
	if order := cmp.Compare(left.distance, right.distance); order != 0 {
		return order
	}
	return cmp.Compare(left.result.Document.ID, right.result.Document.ID)
}

func (s *Store) validateKey(id string) error {
	if id == "" || len(id) > s.maximumKeyBytes || !utf8.ValidString(id) {
		return fmt.Errorf("couchbase: %w: KV key must contain 1..%d bytes", vectorstore.ErrInvalidDocument, s.maximumKeyBytes)
	}
	return nil
}
