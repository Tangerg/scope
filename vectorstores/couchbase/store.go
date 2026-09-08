package couchbase

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/couchbase/gocb/v2"
	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// These values define the persisted Search index topology. Keeping them stable
// avoids silently replacing index internals during client upgrades.
const (
	searchIndexSegmentVersion    = 16
	searchIndexMaximumPartitions = 1024
	searchIndexPartitionCount    = 1
)

// Provider is the stable backend name for host-side attribution.
const Provider = "Couchbase"

// Exported defaults keep constructor behavior visible and overridable.
const (
	DefaultScopeName      = "_default"
	DefaultCollectionName = "_default"
	DefaultIndexName      = "scope-vector-index"
	DefaultSimilarity     = SimilarityDotProduct
	DefaultIndexOptimize  = OptimizeRecall
	contentField          = "content"
	embeddingField        = "embedding"
	metadataField         = "metadata"
	idField               = "id"
	resultScoreField      = "_scope_score"
)

// Similarity selects the vector similarity function written into the
// Couchbase search-index definition.
type Similarity string

const (
	// SimilarityCosine — cosine similarity.
	SimilarityCosine Similarity = "cosine"

	// SimilarityL2Norm — L2 (Euclidean) norm.
	SimilarityL2Norm Similarity = "l2_norm"

	// SimilarityDotProduct — dot product. Default; works
	// best with already-normalized embeddings (e.g. OpenAI).
	SimilarityDotProduct Similarity = "dot_product"
)

func (s Similarity) Valid() bool {
	switch s {
	case SimilarityCosine, SimilarityL2Norm, SimilarityDotProduct:
		return true
	default:
		return false
	}
}

func (s Similarity) String() string { return string(s) }

// IndexOptimization picks the tradeoff for Couchbase's vector index:
// recall (default), latency, or memory.
type IndexOptimization string

// These are the provider values this adapter recognizes.
const (
	OptimizeRecall  IndexOptimization = "recall"
	OptimizeLatency IndexOptimization = "latency"
	OptimizeMemory  IndexOptimization = "memory"
)

func (i IndexOptimization) Valid() bool {
	switch i {
	case OptimizeRecall, OptimizeLatency, OptimizeMemory:
		return true
	default:
		return false
	}
}

func (i IndexOptimization) String() string { return string(i) }

// StoreConfig contains configuration options for the Couchbase Search
// vector store.
type StoreConfig struct {
	// Cluster is the connected gocb cluster. Required.
	Cluster *gocb.Cluster

	// BucketName is the Couchbase bucket. Required.
	BucketName string

	// ScopeName is the scope within the bucket. Optional: defaults
	// to [DefaultScopeName] ("_default").
	ScopeName string

	// CollectionName is the collection within the scope. Optional:
	// defaults to [DefaultCollectionName] ("_default").
	CollectionName string

	// VectorIndexName is the search-index name. Optional: defaults
	// to [DefaultIndexName].
	VectorIndexName string

	// EmbeddingModel produces vectors for the documents. Required.
	EmbeddingModel embedding.Model

	// DocumentBatcher batches documents before upsert. Required.
	DocumentBatcher vectorstore.Batcher

	// Dimensions sets the vector width registered with the search index. When
	// zero and InitializeSchema is true, the store probes EmbeddingModel.
	Dimensions int

	// Similarity selects the vector similarity function. Optional:
	// defaults to [SimilarityDotProduct].
	Similarity Similarity

	// IndexOptimization selects recall / latency / memory tradeoff.
	// Optional: defaults to [OptimizeRecall].
	IndexOptimization IndexOptimization

	// InitializeSchema, when true, creates the search index if it
	// doesn't already exist.
	InitializeSchema bool
}

func (s StoreConfig) Validate() error {
	s.applyDefaults()
	if s.Cluster == nil {
		return errors.New("couchbase: Cluster is required")
	}
	if s.BucketName == "" {
		return errors.New("couchbase: BucketName is required")
	}
	if lo.IsNil(s.EmbeddingModel) {
		return errors.New("couchbase: EmbeddingModel is required")
	}
	if lo.IsNil(s.DocumentBatcher) {
		return errors.New("couchbase: DocumentBatcher is required")
	}
	if s.Dimensions < 0 {
		return errors.New("couchbase: Dimensions must be >= 0")
	}
	if !s.Similarity.Valid() {
		return fmt.Errorf("couchbase: unsupported Similarity %q", s.Similarity)
	}
	if !s.IndexOptimization.Valid() {
		return fmt.Errorf("couchbase: unsupported IndexOptimization %q", s.IndexOptimization)
	}
	return s.validateIdentifiers()
}

func (s StoreConfig) validateIdentifiers() error {
	if err := identifier(s.BucketName).validate("BucketName"); err != nil {
		return err
	}
	if err := identifier(s.ScopeName).validate("ScopeName"); err != nil {
		return err
	}
	if err := identifier(s.CollectionName).validate("CollectionName"); err != nil {
		return err
	}
	return identifier(s.VectorIndexName).validate("VectorIndexName")
}

// applyDefaults fills zero fields with documented defaults.
func (s *StoreConfig) applyDefaults() {
	s.ScopeName = cmp.Or(s.ScopeName, DefaultScopeName)
	s.CollectionName = cmp.Or(s.CollectionName, DefaultCollectionName)
	s.VectorIndexName = cmp.Or(s.VectorIndexName, DefaultIndexName)
	s.Similarity = cmp.Or(s.Similarity, DefaultSimilarity)
	s.IndexOptimization = cmp.Or(s.IndexOptimization, DefaultIndexOptimize)
}

var (
	_ vectorstore.Indexer       = (*Store)(nil)
	_ vectorstore.Searcher      = (*Store)(nil)
	_ vectorstore.FilterDeleter = (*Store)(nil)
	_ vectorstore.IDDeleter     = (*Store)(nil)
)

// Store implements vector-store capabilities with Couchbase Search Service.
type Store struct {
	cluster           *gocb.Cluster
	bucket            *gocb.Bucket
	scope             *gocb.Scope
	collection        *gocb.Collection
	bucketName        string
	scopeName         string
	collectionName    string
	vectorIndexName   string
	embeddingClient   embeddingclient.Client
	documentBatcher   vectorstore.Batcher
	dimensions        int
	similarity        Similarity
	indexOptimization IndexOptimization
}

// NewStore performs schema setup during construction, which is why it takes
// a context: a store returned before its scope, collection, and search index
// exist would fail on the first index rather than at wiring, where the
// misconfiguration actually is.
func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	config.applyDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}

	embeddingClient, err := embeddingclient.New(config.EmbeddingModel)
	if err != nil {
		return nil, fmt.Errorf("couchbase: create embedding client: %w", err)
	}

	bucket := config.Cluster.Bucket(config.BucketName)
	scope := bucket.Scope(config.ScopeName)
	collection := scope.Collection(config.CollectionName)

	store := &Store{
		cluster:           config.Cluster,
		bucket:            bucket,
		scope:             scope,
		collection:        collection,
		bucketName:        config.BucketName,
		scopeName:         config.ScopeName,
		collectionName:    config.CollectionName,
		vectorIndexName:   config.VectorIndexName,
		embeddingClient:   embeddingClient,
		documentBatcher:   config.DocumentBatcher,
		dimensions:        config.Dimensions,
		similarity:        config.Similarity,
		indexOptimization: config.IndexOptimization,
	}

	if err = store.initialize(ctx, config.InitializeSchema); err != nil {
		return nil, fmt.Errorf("couchbase: initialize store: %w", err)
	}
	return store, nil
}

// initialize resolves dimensions and creates the search index when
// requested.
func (s *Store) initialize(ctx context.Context, initSchema bool) error {
	if !initSchema {
		return nil
	}
	if s.dimensions <= 0 {
		return errors.New("couchbase: Dimensions must be > 0")
	}

	return s.upsertSearchIndex()
}

// upsertSearchIndex creates (or refreshes) the FTS index used for
// vector + content search. The index definition mirrors the one
// the framework generates.
func (s *Store) upsertSearchIndex() error {
	mgr := s.scope.SearchIndexes()
	if existing, err := mgr.GetIndex(s.vectorIndexName, nil); err == nil && existing != nil {
		return nil
	}

	typeKey := s.scopeName + "." + s.collectionName
	params := map[string]any{
		"doc_config": map[string]any{
			"docid_prefix_delim": "",
			"docid_regexp":       "",
			"mode":               "scope.collection.type_field",
			"type_field":         "type",
		},
		"mapping": map[string]any{
			"default_analyzer":        "standard",
			"default_datetime_parser": "dateTimeOptional",
			"default_field":           "_all",
			"default_mapping": map[string]any{
				"dynamic": false,
				"enabled": false,
			},
			"default_type":      typeKey,
			"docvalues_dynamic": false,
			"index_dynamic":     false,
			"store_dynamic":     false,
			"type_field":        "_type",
			"types": map[string]any{
				typeKey: map[string]any{
					"dynamic": false,
					"enabled": true,
					"properties": map[string]any{
						embeddingField: map[string]any{
							"dynamic": false,
							"enabled": true,
							"fields": []any{
								map[string]any{
									"dims":                       s.dimensions,
									"index":                      true,
									"name":                       embeddingField,
									"similarity":                 string(s.similarity),
									"type":                       "vector",
									"vector_index_optimized_for": string(s.indexOptimization),
								},
							},
						},
						contentField: map[string]any{
							"dynamic": false,
							"enabled": true,
							"fields": []any{
								map[string]any{
									"analyzer":             "keyword",
									"docvalues":            true,
									"include_in_all":       true,
									"include_term_vectors": true,
									"index":                true,
									"name":                 contentField,
									"store":                true,
									"type":                 "text",
								},
							},
						},
					},
				},
			},
		},
		"store": map[string]any{
			"indexType":      "scorch",
			"segmentVersion": searchIndexSegmentVersion,
		},
	}

	idx := gocb.SearchIndex{
		Name:       s.vectorIndexName,
		SourceName: s.bucketName,
		Type:       "fulltext-index",
		SourceType: "gocbcore",
		Params:     params,
		PlanParams: map[string]any{
			"maxPartitionsPerPIndex": searchIndexMaximumPartitions,
			"indexPartitions":        searchIndexPartitionCount,
		},
		SourceParams: map[string]any{},
	}
	return mgr.UpsertIndex(idx, nil)
}

// Index embeds documents and upserts them by id.
func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) (err error) {
	if validateErr := request.Validate(); validateErr != nil {
		return fmt.Errorf("couchbase.Store.Index: %w", validateErr)
	}

	var batches []*vectorstore.IndexRequest
	batches, err = request.Batch(ctx, s.documentBatcher)
	if err != nil {
		return fmt.Errorf("couchbase: batch documents: %w", err)
	}

	for _, batch := range batches {
		docs := batch.Documents
		texts, err := batch.Texts()
		if err != nil {
			return fmt.Errorf("vectorstore: project document text: %w", err)
		}
		vectors, err := s.embeddingClient.EmbedTexts(ctx, texts)
		if err != nil {
			return fmt.Errorf("couchbase: embed documents: %w", err)
		}

		for i, doc := range docs {
			id := doc.ID
			metadataValues, err := doc.Metadata.Values()
			if err != nil {
				return fmt.Errorf("couchbase: decode metadata for %s: %w", id, err)
			}
			payload := map[string]any{
				idField:        id,
				contentField:   doc.Text,
				metadataField:  lo.CoalesceMapOrEmpty(metadataValues),
				embeddingField: embedding.Float32Vector(vectors[i]),
			}
			if _, err := s.collection.Upsert(id, payload, &gocb.UpsertOptions{Context: ctx}); err != nil {
				return fmt.Errorf("couchbase: upsert %s: %w", id, err)
			}
		}
	}
	return nil
}

// Search runs a SQL++ query that embeds the KNN search clause.
func (s *Store) Search(ctx context.Context, req *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	var docs []*vectorstore.SearchResult
	if err = req.Validate(); err != nil {
		return nil, fmt.Errorf("couchbase.Store.Search: %w", err)
	}
	if err = req.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, fmt.Errorf("couchbase.Store.Search: %w", err)
	}

	defer func() {
		if err == nil {
			err = response.ValidateFor(req)
		}
	}()

	vector, err := s.embeddingClient.EmbedText(ctx, req.Query)
	if err != nil {
		return nil, fmt.Errorf("couchbase: embed query: %w", err)
	}
	queryVec := embedding.Float32Vector(vector)
	vectorJSON, err := json.Marshal(queryVec)
	if err != nil {
		return nil, fmt.Errorf("couchbase: encode query vector: %w", err)
	}

	whereExtra := ""
	if req.Options.Filter != nil {
		predicate, filterErr := s.buildFilter(req.Options.Filter)
		if filterErr != nil {
			return nil, filterErr
		}
		if predicate != "" {
			whereExtra = " AND " + predicate
		}
	}

	knnFragment := fmt.Sprintf(
		`{"query":{"match_none":{}},"knn":[{"field":"%s","k":%d,"vector":%s}]}`,
		embeddingField, req.Options.ResultLimit(), string(vectorJSON),
	)
	indexFullName := fmt.Sprintf("%s.%s.%s", s.bucketName, s.scopeName, s.vectorIndexName)
	stmt := fmt.Sprintf(
		`SELECT c.*, SEARCH_SCORE() AS %s FROM `+"`%s`"+`.`+"`%s`"+`.`+"`%s`"+` AS c `+
			`WHERE SEARCH(c, %s, {"index": "%s"})%s ORDER BY SEARCH_SCORE() DESC LIMIT %d`,
		resultScoreField,
		s.bucketName, s.scopeName, s.collectionName,
		knnFragment, indexFullName, whereExtra, req.Options.ResultLimit(),
	)

	docs = make([]*vectorstore.SearchResult, 0, req.Options.ResultLimit())
	if err = s.runStatement(ctx, stmt, func(result *gocb.QueryResult) error {
		var raw map[string]any
		if rowErr := result.Row(&raw); rowErr != nil {
			return fmt.Errorf("couchbase: decode row: %w", rowErr)
		}
		doc, docErr := s.toDocument(raw)
		if docErr != nil {
			return docErr
		}
		rawScore, ok := raw[resultScoreField].(float64)
		if !ok {
			return fmt.Errorf("couchbase: result is missing numeric %s", resultScoreField)
		}
		score := vectorstore.ScoreFromValue(rawScore)
		if score < req.Options.MinScore {
			return nil
		}
		docs = append(docs, &vectorstore.SearchResult{Document: doc, Score: score})
		return nil
	}); err != nil {
		return nil, err
	}
	return &vectorstore.SearchResponse{Results: docs}, nil
}

// runStatement executes one N1QL statement and accounts for its complete
// result. gocb surfaces failures raised while the result streams through Err
// and Close rather than from Query itself, so a statement whose first response
// succeeded can still have failed; an unclosed result also leaks its stream.
// Routing both reads and mutations through one owner keeps that accounting from
// being implemented on only some of the paths. A nil row function consumes a
// statement that returns no rows.
func (s *Store) runStatement(
	ctx context.Context,
	stmt string,
	row func(*gocb.QueryResult) error,
) (err error) {
	result, queryErr := s.scope.Query(stmt, &gocb.QueryOptions{Context: ctx})
	if queryErr != nil {
		return fmt.Errorf("couchbase: query: %w", queryErr)
	}
	defer func() {
		if closeErr := result.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("couchbase: close result: %w", closeErr))
		}
	}()
	if row != nil {
		for result.Next() {
			if rowErr := row(result); rowErr != nil {
				return rowErr
			}
		}
	}
	if streamErr := result.Err(); streamErr != nil {
		return fmt.Errorf("couchbase: read rows: %w", streamErr)
	}
	return nil
}

func (s *Store) DeleteWhere(ctx context.Context, expr filter.Predicate) (err error) {
	if expr == nil {
		return vectorstore.ErrMissingFilter
	}
	if err = expr.Validate(); err != nil {
		return fmt.Errorf("couchbase.Store.DeleteWhere: %w", err)
	}

	predicate, err := s.buildFilter(expr)
	if err != nil {
		return err
	}
	if predicate == "" {
		return errors.New("couchbase: refusing to delete on empty filter")
	}

	stmt := fmt.Sprintf(
		`DELETE FROM `+"`%s`"+`.`+"`%s`"+`.`+"`%s`"+` WHERE %s`,
		s.bucketName, s.scopeName, s.collectionName, predicate,
	)
	if err := s.runStatement(ctx, stmt, nil); err != nil {
		return fmt.Errorf("couchbase: delete: %w", err)
	}
	return nil
}

// DeleteIDs removes documents by their KV key. Index upserts each
// document under its id as the document key (see [Store.Index]), so the
// id is the KV key here too. An empty slice is a no-op; a per-key
// "document not found" error is treated as success so repeated deletes
// stay idempotent. Implements [vectorstore.IDDeleter].
func (s *Store) DeleteIDs(ctx context.Context, ids []string) (err error) {
	if len(ids) == 0 {
		return nil
	}

	for _, id := range ids {
		if _, removeErr := s.collection.Remove(id, &gocb.RemoveOptions{Context: ctx}); removeErr != nil {
			if errors.Is(removeErr, gocb.ErrDocumentNotFound) {
				continue
			}
			return fmt.Errorf("couchbase: remove %s: %w", id, removeErr)
		}
	}
	return nil
}

// buildFilter wraps the visitor.
func (s *Store) buildFilter(expr filter.Predicate) (string, error) {
	if expr == nil {
		return "", nil
	}
	v := newVisitor(metadataField)
	if err := expr.Accept(v); err != nil {
		return "", fmt.Errorf("couchbase: convert filter: %w", err)
	}
	return v.snapshot(), nil
}

func (s *Store) toDocument(raw map[string]any) (*document.Document, error) {
	id, ok := raw[idField].(string)
	if !ok || id == "" {
		return nil, fmt.Errorf("couchbase: result is missing string field %q", idField)
	}
	content, ok := raw[contentField].(string)
	if !ok || content == "" {
		return nil, fmt.Errorf("couchbase: result is missing string field %q", contentField)
	}
	doc := &document.Document{ID: id, Text: content}
	if meta, ok := raw[metadataField].(map[string]any); ok {
		var err error
		doc.Metadata, err = metadata.FromValues(meta)
		if err != nil {
			return nil, fmt.Errorf("couchbase: convert metadata: %w", err)
		}
	}
	return doc, nil
}
