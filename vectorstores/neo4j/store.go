package neo4j

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

const Provider = "Neo4j"

const (
	DefaultLabel             = "Document"
	DefaultEmbeddingProperty = "embedding"
	DefaultIDProperty        = "id"
	DefaultTextProperty      = "text"
	DefaultMetadataProperty  = "metadata"
	transactionBatchSize     = 512
	sessionCloseTimeout      = 10 * time.Second
)

// Driver creates sessions; its connection pool remains owned by the host.
type Driver interface {
	NewSession(context.Context, neo4j.SessionConfig) neo4j.SessionWithContext
}

// SimilarityFunction selects Neo4j's native exact similarity function.
type SimilarityFunction string

const (
	SimilarityCosine    SimilarityFunction = "cosine"
	SimilarityEuclidean SimilarityFunction = "euclidean"
)

func (s SimilarityFunction) Valid() bool {
	return s == SimilarityCosine || s == SimilarityEuclidean
}
func (s SimilarityFunction) String() string { return string(s) }

// StoreConfig selects an exclusively owned document label and its four properties.
// Empty property names use their exported defaults. Driver, EmbeddingModel and
// DocumentBatcher are required.
type StoreConfig struct {
	Driver Driver
	// Database is optional; empty uses the driver's default database.
	Database string
	// Label defaults to DefaultLabel. Every node with this label must use the current schema.
	Label             string
	EmbeddingProperty string
	IDProperty        string
	TextProperty      string
	// MetadataProperty stores the entire Core JSON map, including null or {}.
	MetadataProperty string
	EmbeddingModel   embedding.Model
	DocumentBatcher  vectorstore.Batcher
	// Similarity defaults to SimilarityCosine.
	Similarity SimilarityFunction
	// InitializeSchema creates the ID uniqueness constraint. Construction always
	// verifies the actual constraint, including when schema creation is disabled.
	InitializeSchema bool
}

func (s StoreConfig) Validate() error {
	s.applyDefaults()
	if lo.IsNil(s.Driver) {
		return errors.New("neo4j: Driver is required")
	}
	if lo.IsNil(s.EmbeddingModel) {
		return errors.New("neo4j: EmbeddingModel is required")
	}
	if lo.IsNil(s.DocumentBatcher) {
		return errors.New("neo4j: DocumentBatcher is required")
	}
	if !s.Similarity.Valid() {
		return fmt.Errorf("neo4j: unsupported Similarity %q", s.Similarity)
	}
	return s.validateIdentifiers()
}

func (s StoreConfig) validateIdentifiers() error {
	if err := identifier(s.Label).validate("Label"); err != nil {
		return err
	}
	fields := []struct{ name, value string }{
		{"IDProperty", s.IDProperty}, {"TextProperty", s.TextProperty},
		{"EmbeddingProperty", s.EmbeddingProperty}, {"MetadataProperty", s.MetadataProperty},
	}
	seen := make(map[string]string, len(fields))
	for _, field := range fields {
		if err := identifier(field.value).validate(field.name); err != nil {
			return err
		}
		if owner, duplicate := seen[field.value]; duplicate {
			return fmt.Errorf("neo4j: %s and %s both use property %q", owner, field.name, field.value)
		}
		seen[field.value] = field.name
	}
	return nil
}

func (s *StoreConfig) applyDefaults() {
	s.Label = cmp.Or(s.Label, DefaultLabel)
	s.EmbeddingProperty = cmp.Or(s.EmbeddingProperty, DefaultEmbeddingProperty)
	s.IDProperty = cmp.Or(s.IDProperty, DefaultIDProperty)
	s.TextProperty = cmp.Or(s.TextProperty, DefaultTextProperty)
	s.MetadataProperty = cmp.Or(s.MetadataProperty, DefaultMetadataProperty)
	s.Similarity = cmp.Or(s.Similarity, SimilarityCosine)
}

var (
	_ vectorstore.Indexer       = (*Store)(nil)
	_ vectorstore.Searcher      = (*Store)(nil)
	_ vectorstore.FilterDeleter = (*Store)(nil)
	_ vectorstore.IDDeleter     = (*Store)(nil)
)

// Store projects Core documents onto Neo4j nodes. Core owns filter semantics;
// native transactions own publication and node locks, and Neo4j owns similarity.
type Store struct {
	driver            Driver
	database          string
	label             string
	embeddingProperty string
	idProperty        string
	textProperty      string
	metadataProperty  string
	embeddingClient   embeddingclient.Client
	documentBatcher   vectorstore.Batcher
	similarity        SimilarityFunction
}

func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	config.applyDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}
	client, err := embeddingclient.New(config.EmbeddingModel)
	if err != nil {
		return nil, fmt.Errorf("neo4j: create embedding client: %w", err)
	}
	store := &Store{
		driver: config.Driver, database: config.Database, label: config.Label,
		embeddingProperty: config.EmbeddingProperty, idProperty: config.IDProperty,
		textProperty: config.TextProperty, metadataProperty: config.MetadataProperty,
		embeddingClient: client, documentBatcher: config.DocumentBatcher, similarity: config.Similarity,
	}
	if err := store.initialize(ctx, config.InitializeSchema); err != nil {
		return nil, fmt.Errorf("neo4j: initialize: %w", err)
	}
	return store, nil
}

func (s *Store) initialize(ctx context.Context, create bool) error {
	if create {
		query := fmt.Sprintf("CREATE CONSTRAINT IF NOT EXISTS FOR (n:%s) REQUIRE n.%s IS UNIQUE", quoteIdentifier(s.label), quoteIdentifier(s.idProperty))
		if _, err := s.transact(ctx, neo4j.AccessModeWrite, func(tx neo4j.ManagedTransaction) (any, error) {
			result, err := tx.Run(ctx, query, nil)
			if err != nil {
				return nil, err
			}
			return result.Consume(ctx)
		}); err != nil {
			return err
		}
	}
	_, err := s.transact(ctx, neo4j.AccessModeRead, func(tx neo4j.ManagedTransaction) (any, error) {
		result, err := tx.Run(ctx, "SHOW CONSTRAINTS YIELD type, entityType, labelsOrTypes, properties WHERE type = 'UNIQUENESS' AND entityType = 'NODE' AND labelsOrTypes = [$label] AND properties = [$property] RETURN count(*) AS count", map[string]any{"label": s.label, "property": s.idProperty})
		if err != nil {
			return nil, err
		}
		record, err := result.Single(ctx)
		if err != nil {
			return nil, err
		}
		count, ok := record.Get("count")
		if !ok || count != int64(1) {
			return nil, errors.New("neo4j: document ID requires a native uniqueness constraint")
		}
		// Capability discovery must fail at construction, not on the first search.
		_, err = s.scoreVectors(ctx, tx, [][]float64{{1, 0}}, nil)
		return nil, err
	})
	return err
}

func (s *Store) transact(ctx context.Context, mode neo4j.AccessMode, work neo4j.ManagedTransactionWork) (value any, err error) {
	session := s.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: mode, DatabaseName: s.database})
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sessionCloseTimeout)
		defer cancel()
		err = errors.Join(err, session.Close(closeCtx))
		if err != nil {
			err = errors.Join(err, ctx.Err())
			value = nil
		}
	}()
	if mode == neo4j.AccessModeWrite {
		return session.ExecuteWrite(ctx, work)
	}
	return session.ExecuteRead(ctx, work)
}

// Index embeds outside the SDK's retryable transaction and publishes the complete
// request in one native transaction. No model call is repeated by a database retry.
func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) error {
	if err := request.Validate(); err != nil {
		return fmt.Errorf("neo4j: index: %w", err)
	}
	for i, doc := range request.Documents {
		if doc.Media != nil {
			return fmt.Errorf("neo4j: index: %w: documents[%d] contains unsupported media", vectorstore.ErrInvalidDocument, i)
		}
	}
	batches, batchErr := request.Batch(ctx, s.documentBatcher)
	if batchErr != nil {
		return fmt.Errorf("neo4j: batch: %w", batchErr)
	}
	rows := make([]map[string]any, 0, len(request.Documents))
	vectors := make([][]float64, 0, len(request.Documents))
	for _, batch := range batches {
		texts, err := batch.Texts()
		if err != nil {
			return err
		}
		embedded, err := s.embeddingClient.EmbedTexts(ctx, texts)
		if err != nil {
			return fmt.Errorf("neo4j: embed: %w", err)
		}
		for i, doc := range batch.Documents {
			props, err := s.documentProperties(doc, embedded[i])
			if err != nil {
				return err
			}
			rows = append(rows, props)
			vectors = append(vectors, embedded[i])
		}
	}
	query := fmt.Sprintf("UNWIND $rows AS row MERGE (n:%s {%s: row[$idProperty]}) SET n = row", quoteIdentifier(s.label), quoteIdentifier(s.idProperty))
	_, err := s.transact(ctx, neo4j.AccessModeWrite, func(tx neo4j.ManagedTransaction) (any, error) {
		// The native function owns metric-specific vector validity, including cosine's nonzero norm.
		if _, err := s.scoreVectors(ctx, tx, vectors, nil); err != nil {
			return nil, err
		}
		for batch := range slices.Chunk(rows, transactionBatchSize) {
			result, err := tx.Run(ctx, query, map[string]any{"rows": batch, "idProperty": s.idProperty})
			if err != nil {
				return nil, err
			}
			if _, err := result.Consume(ctx); err != nil {
				return nil, err
			}
		}
		return nil, nil
	})
	if err != nil {
		return fmt.Errorf("neo4j: upsert: %w", err)
	}
	return nil
}

func (s *Store) documentProperties(doc *document.Document, vector []float64) (map[string]any, error) {
	encoded, err := doc.Metadata.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("neo4j: encode metadata for %q: %w", doc.ID, err)
	}
	return map[string]any{s.idProperty: doc.ID, s.textProperty: doc.Text, s.metadataProperty: string(encoded), s.embeddingProperty: vector}, nil
}

// storedNode captures properties once. elementID is Neo4j's transaction-local
// storage handle, not another representation of the document's business ID.
type storedNode struct {
	document  *document.Document
	vector    []float64
	elementID string
}

func (s *Store) decodeRecord(record *neo4j.Record) (*storedNode, error) {
	raw, _ := record.Get("properties")
	props, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("neo4j: stored properties have type %T", raw)
	}
	if len(props) != 4 {
		return nil, errors.New("neo4j: node must contain exactly the four current document properties")
	}
	id, idOK := props[s.idProperty].(string)
	text, textOK := props[s.textProperty].(string)
	encoded, metadataOK := props[s.metadataProperty].(string)
	if !idOK || strings.TrimSpace(id) == "" || !textOK || !metadataOK {
		return nil, errors.New("neo4j: invalid document property types or ID")
	}
	var decoded metadata.Map
	if err := decoded.UnmarshalJSON([]byte(encoded)); err != nil {
		return nil, fmt.Errorf("neo4j: decode metadata for %q: %w", id, err)
	}
	doc := &document.Document{ID: id, Text: text, Metadata: decoded}
	if err := doc.Validate(); err != nil {
		return nil, err
	}
	rawVector, ok := props[s.embeddingProperty].([]any)
	if !ok {
		return nil, fmt.Errorf("neo4j: node %q has invalid embedding type %T", id, props[s.embeddingProperty])
	}
	vector := make([]float64, len(rawVector))
	for i, item := range rawVector {
		value, isFloat := item.(float64)
		if !isFloat {
			return nil, fmt.Errorf("neo4j: node %q embedding[%d] has type %T, want FLOAT", id, i, item)
		}
		vector[i] = value
	}
	if err := (&embedding.Output{Embedding: vector}).Validate(); err != nil {
		return nil, err
	}
	elementID, _ := record.Get("elementID")
	handle, ok := elementID.(string)
	if !ok || handle == "" {
		return nil, errors.New("neo4j: missing node element ID")
	}
	score, _ := record.Get("selfScore")
	value, ok := score.(float64)
	if !ok {
		return nil, fmt.Errorf("neo4j: native vector validity score has type %T", score)
	}
	if err := vectorstore.Score(value).Validate(); err != nil {
		return nil, err
	}
	return &storedNode{document: doc, vector: vector, elementID: handle}, nil
}

func (s *Store) selectNodes(ctx context.Context, tx neo4j.ManagedTransaction, predicate filter.Predicate, lock bool) ([]*storedNode, error) {
	lockClause := ""
	if lock {
		// A dependent SET acquires the node write lock before the property read;
		// the native transaction retains that lock through validation and deletion.
		lockClause = fmt.Sprintf(" SET n.%s = n.%s", quoteIdentifier(s.idProperty), quoteIdentifier(s.idProperty))
	}
	query := fmt.Sprintf("MATCH (n:%s)%s WITH n, properties(n) AS stored RETURN elementId(n) AS elementID, stored AS properties, vector.similarity.%s(stored[$embeddingProperty], stored[$embeddingProperty]) AS selfScore", quoteIdentifier(s.label), lockClause, s.similarity)
	result, err := tx.Run(ctx, query, map[string]any{"embeddingProperty": s.embeddingProperty})
	if err != nil {
		return nil, err
	}
	var selected []*storedNode
	seen := make(map[string]struct{})
	for result.Next(ctx) {
		node, err := s.decodeRecord(result.Record())
		if err != nil {
			return nil, err
		}
		if _, exists := seen[node.document.ID]; exists {
			return nil, fmt.Errorf("neo4j: duplicate document ID %q", node.document.ID)
		}
		seen[node.document.ID] = struct{}{}
		if predicate == nil {
			selected = append(selected, node)
			continue
		}
		values, err := node.document.Metadata.Values()
		if err != nil {
			return nil, err
		}
		match, err := filter.Match(predicate, values)
		if err != nil {
			return nil, fmt.Errorf("neo4j: filter node %q: %w", node.document.ID, err)
		}
		if match {
			selected = append(selected, node)
		}
	}
	if err := result.Err(); err != nil {
		return nil, err
	}
	return selected, nil
}

func (s *Store) scoreVectors(ctx context.Context, tx neo4j.ManagedTransaction, vectors [][]float64, queryVector []float64) ([]vectorstore.Score, error) {
	operand := "row.embedding"
	if queryVector != nil {
		operand = "$query"
	}
	query := fmt.Sprintf("UNWIND $rows AS row RETURN row.position AS position, vector.similarity.%s(row.embedding, %s) AS score", s.similarity, operand)
	scores := make([]vectorstore.Score, len(vectors))
	for start := 0; start < len(vectors); start += transactionBatchSize {
		end := min(start+transactionBatchSize, len(vectors))
		rows := make([]map[string]any, end-start)
		for i := start; i < end; i++ {
			rows[i-start] = map[string]any{"position": int64(i - start), "embedding": vectors[i]}
		}
		result, err := tx.Run(ctx, query, map[string]any{"rows": rows, "query": queryVector})
		if err != nil {
			return nil, err
		}
		seen := make([]bool, len(rows))
		count := 0
		for result.Next(ctx) {
			record := result.Record()
			raw, _ := record.Get("position")
			position, ok := raw.(int64)
			if !ok || position < 0 || position >= int64(len(rows)) || seen[position] {
				return nil, errors.New("neo4j: invalid or duplicate native score position")
			}
			raw, _ = record.Get("score")
			value, ok := raw.(float64)
			if !ok {
				return nil, fmt.Errorf("neo4j: native score has type %T, want FLOAT", raw)
			}
			score := vectorstore.Score(value)
			if err := score.Validate(); err != nil {
				return nil, err
			}
			scores[start+int(position)] = score
			seen[position] = true
			count++
		}
		if err := result.Err(); err != nil {
			return nil, err
		}
		if count != len(rows) {
			return nil, errors.New("neo4j: native score count differs from candidate count")
		}
	}
	return scores, nil
}

func (s *Store) Search(ctx context.Context, request *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	if err = request.Validate(); err != nil {
		return nil, fmt.Errorf("neo4j: search: %w", err)
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
	value, err := s.transact(ctx, neo4j.AccessModeRead, func(tx neo4j.ManagedTransaction) (any, error) {
		return s.selectNodes(ctx, tx, request.Options.Filter, false)
	})
	if err != nil {
		return nil, fmt.Errorf("neo4j: read candidates: %w", err)
	}
	selected := value.([]*storedNode)
	if len(selected) == 0 {
		return &vectorstore.SearchResponse{}, nil
	}
	queryVector, err := s.embeddingClient.EmbedText(ctx, request.Query)
	if err != nil {
		return nil, fmt.Errorf("neo4j: embed query: %w", err)
	}
	vectors := make([][]float64, len(selected))
	for i, node := range selected {
		vectors[i] = node.vector
	}
	value, err = s.transact(ctx, neo4j.AccessModeRead, func(tx neo4j.ManagedTransaction) (any, error) { return s.scoreVectors(ctx, tx, vectors, queryVector) })
	if err != nil {
		return nil, fmt.Errorf("neo4j: score candidates: %w", err)
	}
	scores := value.([]vectorstore.Score)
	results := make([]*vectorstore.SearchResult, 0, len(selected))
	for i, node := range selected {
		result, err := vectorstore.NewSearchResult(node.document, scores[i])
		if err != nil {
			return nil, err
		}
		if result.Score >= request.Options.MinScore {
			results = append(results, result)
		}
	}
	slices.SortFunc(results, func(left, right *vectorstore.SearchResult) int {
		if order := cmp.Compare(right.Score, left.Score); order != 0 {
			return order
		}
		return strings.Compare(left.Document.ID, right.Document.ID)
	})
	results = results[:min(len(results), request.Options.ResultLimit())]
	return &vectorstore.SearchResponse{Results: results}, nil
}

// DeleteWhere validates the entire observed collection before any deletion.
// Node write locks prevent an observed match being replaced before deletion.
func (s *Store) DeleteWhere(ctx context.Context, predicate filter.Predicate) error {
	if predicate == nil {
		return vectorstore.ErrMissingFilter
	}
	if err := predicate.Validate(); err != nil {
		return fmt.Errorf("neo4j: delete: %w", err)
	}
	_, err := s.transact(ctx, neo4j.AccessModeWrite, func(tx neo4j.ManagedTransaction) (any, error) {
		nodes, err := s.selectNodes(ctx, tx, predicate, true)
		if err != nil {
			return nil, err
		}
		handles := make([]string, len(nodes))
		for i, node := range nodes {
			handles[i] = node.elementID
		}
		query := fmt.Sprintf("MATCH (n:%s) WHERE elementId(n) IN $ids DETACH DELETE n", quoteIdentifier(s.label))
		for batch := range slices.Chunk(handles, transactionBatchSize) {
			result, err := tx.Run(ctx, query, map[string]any{"ids": batch})
			if err != nil {
				return nil, err
			}
			if _, err := result.Consume(ctx); err != nil {
				return nil, err
			}
		}
		return nil, nil
	})
	if err != nil {
		return fmt.Errorf("neo4j: delete matches: %w", err)
	}
	return nil
}

func (s *Store) DeleteIDs(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	query := fmt.Sprintf("MATCH (n:%s) WHERE n.%s IN $ids DETACH DELETE n", quoteIdentifier(s.label), quoteIdentifier(s.idProperty))
	_, err := s.transact(ctx, neo4j.AccessModeWrite, func(tx neo4j.ManagedTransaction) (any, error) {
		result, err := tx.Run(ctx, query, map[string]any{"ids": ids})
		if err != nil {
			return nil, err
		}
		return result.Consume(ctx)
	})
	if err != nil {
		return fmt.Errorf("neo4j: delete IDs: %w", err)
	}
	return nil
}
