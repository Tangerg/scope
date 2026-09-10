package neo4j

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// Provider is the stable backend name for host-side attribution.
const Provider = "Neo4j"

// Exported defaults keep constructor behavior visible and overridable.
const (
	DefaultLabel             = "Document"
	DefaultIndexName         = "scope-vector-index"
	DefaultEmbeddingProperty = "embedding"
	DefaultIDProperty        = "id"
	DefaultTextProperty      = "text"
	DefaultMetadataPrefix    = "metadata"
)

// SimilarityFunction selects the function written into the vector
// index definition. The chosen value is recorded at index creation
// time and cannot be changed without rebuilding the index.
type SimilarityFunction string

const (
	// SimilarityCosine — cosine similarity. Default.
	SimilarityCosine SimilarityFunction = "cosine"

	// SimilarityEuclidean — Euclidean distance, mapped to a [0, 1]
	// similarity score by Neo4j itself.
	SimilarityEuclidean SimilarityFunction = "euclidean"
)

func (s SimilarityFunction) Valid() bool {
	return s == SimilarityCosine || s == SimilarityEuclidean
}

func (s SimilarityFunction) String() string { return string(s) }

// StoreConfig contains configuration options for the Neo4j vector
// store.
type StoreConfig struct {
	// Driver is the Neo4j context-aware driver instance. Required.
	Driver neo4j.DriverWithContext

	// Database is the Neo4j database name. Optional: defaults to the
	// driver's default database (typically "neo4j").
	Database string

	// Label is the node label used for documents. Optional: defaults
	// to [DefaultLabel].
	Label string

	// IndexName is the vector index name. Optional: defaults to
	// [DefaultIndexName].
	IndexName string

	// EmbeddingProperty is the node property that stores the vector.
	// Optional: defaults to [DefaultEmbeddingProperty].
	EmbeddingProperty string

	// IDProperty is the node property that stores the document id.
	// Optional: defaults to [DefaultIDProperty].
	IDProperty string

	// TextProperty is the node property that stores the document
	// text. Optional: defaults to [DefaultTextProperty].
	TextProperty string

	// MetadataPrefix is the property-name prefix used for metadata
	// keys (so "metadata.author" instead of "author"). Optional:
	// defaults to [DefaultMetadataPrefix]. The prefix is always present so
	// metadata keys cannot collide with storage properties.
	MetadataPrefix string

	// EmbeddingModel produces vectors for the documents. Required.
	EmbeddingModel embedding.Model

	// DocumentBatcher batches documents before upsert. Required.
	DocumentBatcher vectorstore.Batcher

	// Dimensions sets the vector width recorded in a new index definition, and
	// is required when InitializeSchema is true: the width is part of the index
	// definition, and nothing here can read it off an index that does not exist
	// yet.
	Dimensions int

	// Similarity selects the vector similarity function. Optional:
	// defaults to [SimilarityCosine].
	Similarity SimilarityFunction

	// InitializeSchema, when true, creates the unique-id constraint
	// and the vector index if they don't already exist.
	InitializeSchema bool
}

func (s StoreConfig) Validate() error {
	s.applyDefaults()
	if s.Driver == nil {
		return errors.New("neo4j: Driver is required")
	}
	if lo.IsNil(s.EmbeddingModel) {
		return errors.New("neo4j: EmbeddingModel is required")
	}
	if lo.IsNil(s.DocumentBatcher) {
		return errors.New("neo4j: DocumentBatcher is required")
	}
	if s.Dimensions < 0 {
		return errors.New("neo4j: Dimensions must be >= 0")
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
	if err := identifier(s.EmbeddingProperty).validate("EmbeddingProperty"); err != nil {
		return err
	}
	if err := identifier(s.IDProperty).validate("IDProperty"); err != nil {
		return err
	}
	if err := identifier(s.TextProperty).validate("TextProperty"); err != nil {
		return err
	}
	fields := []struct {
		name  string
		value string
	}{
		{name: "IDProperty", value: s.IDProperty},
		{name: "TextProperty", value: s.TextProperty},
		{name: "EmbeddingProperty", value: s.EmbeddingProperty},
	}
	seen := make(map[string]string, len(fields))
	for _, field := range fields {
		if owner, duplicate := seen[field.value]; duplicate {
			return fmt.Errorf("neo4j: %s and %s both use property %q", owner, field.name, field.value)
		}
		seen[field.value] = field.name
	}
	return nil
}

// applyDefaults fills zero fields with documented defaults.
func (s *StoreConfig) applyDefaults() {
	s.Label = cmp.Or(s.Label, DefaultLabel)
	s.IndexName = cmp.Or(s.IndexName, DefaultIndexName)
	s.EmbeddingProperty = cmp.Or(s.EmbeddingProperty, DefaultEmbeddingProperty)
	s.IDProperty = cmp.Or(s.IDProperty, DefaultIDProperty)
	s.TextProperty = cmp.Or(s.TextProperty, DefaultTextProperty)
	s.MetadataPrefix = cmp.Or(s.MetadataPrefix, DefaultMetadataPrefix)
	s.Similarity = cmp.Or(s.Similarity, SimilarityCosine)
}

var (
	_ vectorstore.Indexer       = (*Store)(nil)
	_ vectorstore.Searcher      = (*Store)(nil)
	_ vectorstore.FilterDeleter = (*Store)(nil)
	_ vectorstore.IDDeleter     = (*Store)(nil)
)

// Store implements vector-store capabilities with Neo4j. Each document maps to
// a node with the configured label and flattened metadata properties.
type Store struct {
	driver            neo4j.DriverWithContext
	database          string
	label             string
	indexName         string
	embeddingProperty string
	idProperty        string
	textProperty      string
	metadataPrefix    string
	embeddingClient   embeddingclient.Client
	documentBatcher   vectorstore.Batcher
	dimensions        int
	similarity        SimilarityFunction
}

// NewStore performs schema setup during construction, which is why it takes
// a context: a store returned before its graph schema and index exist would
// fail on the first index rather than at wiring, where the misconfiguration
// actually is.
func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	config.applyDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}

	embeddingClient, err := embeddingclient.New(config.EmbeddingModel)
	if err != nil {
		return nil, fmt.Errorf("neo4j: create embedding client: %w", err)
	}

	store := &Store{
		driver:            config.Driver,
		database:          config.Database,
		label:             config.Label,
		indexName:         config.IndexName,
		embeddingProperty: config.EmbeddingProperty,
		idProperty:        config.IDProperty,
		textProperty:      config.TextProperty,
		metadataPrefix:    config.MetadataPrefix,
		embeddingClient:   embeddingClient,
		documentBatcher:   config.DocumentBatcher,
		dimensions:        config.Dimensions,
		similarity:        config.Similarity,
	}

	if err = store.initialize(ctx, config.InitializeSchema); err != nil {
		return nil, fmt.Errorf("neo4j: initialize store: %w", err)
	}
	return store, nil
}

// initialize provisions the vector index
// when requested.
func (s *Store) initialize(ctx context.Context, initSchema bool) error {
	if !initSchema {
		return nil
	}
	if s.dimensions <= 0 {
		return errors.New("neo4j: Dimensions must be > 0")
	}

	constraintName := quoteIdentifier(s.indexName + "_unique")
	indexName := quoteIdentifier(s.indexName)
	constraintStmt := fmt.Sprintf(
		"CREATE CONSTRAINT %s IF NOT EXISTS FOR (n:`%s`) REQUIRE n.`%s` IS UNIQUE",
		constraintName, s.label, s.idProperty,
	)
	indexStmt := fmt.Sprintf(
		"CREATE VECTOR INDEX %s IF NOT EXISTS FOR (n:`%s`) ON (n.`%s`) "+
			"OPTIONS {indexConfig: {`vector.dimensions`: %d, `vector.similarity_function`: '%s'}}",
		indexName, s.label, s.embeddingProperty, s.dimensions, s.similarity,
	)

	return s.write(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		if _, err := tx.Run(ctx, constraintStmt, nil); err != nil {
			return nil, err
		}
		if _, err := tx.Run(ctx, indexStmt, nil); err != nil {
			return nil, err
		}
		return nil, nil
	})
}

// session opens a session bound to the configured database, if any.
func (s *Store) session(ctx context.Context, accessMode neo4j.AccessMode) neo4j.SessionWithContext {
	config := neo4j.SessionConfig{AccessMode: accessMode}
	if s.database != "" {
		config.DatabaseName = s.database
	}
	return s.driver.NewSession(ctx, config)
}

// write runs work inside a managed write transaction.
func (s *Store) write(ctx context.Context, work neo4j.ManagedTransactionWork) error {
	session := s.session(ctx, neo4j.AccessModeWrite)
	defer session.Close(ctx)
	if _, err := session.ExecuteWrite(ctx, work); err != nil {
		return err
	}
	return nil
}

// Index embeds documents and upserts them as nodes.
func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) (err error) {
	if validateErr := request.Validate(); validateErr != nil {
		return fmt.Errorf("neo4j.Store.Index: %w", validateErr)
	}
	for index, doc := range request.Documents {
		if doc.Media != nil {
			return fmt.Errorf("neo4j.Store.Index: %w: documents[%d] contains unsupported media", vectorstore.ErrInvalidDocument, index)
		}
	}

	var batches []*vectorstore.IndexRequest
	batches, err = request.Batch(ctx, s.documentBatcher)
	if err != nil {
		return fmt.Errorf("neo4j: batch documents: %w", err)
	}

	upsertCypher := fmt.Sprintf(
		"UNWIND $rows AS row "+
			"MERGE (n:`%s` {`%s`: row.id}) "+
			"SET n = row.properties "+
			"WITH row, n "+
			"CALL db.create.setNodeVectorProperty(n, $embeddingProperty, row.embedding) "+
			"RETURN count(*)",
		s.label, s.idProperty,
	)

	for _, batch := range batches {
		docs := batch.Documents
		texts, err := batch.Texts()
		if err != nil {
			return fmt.Errorf("vectorstore: project document text: %w", err)
		}
		vectors, err := s.embeddingClient.EmbedTexts(ctx, texts)
		if err != nil {
			return fmt.Errorf("neo4j: embed documents: %w", err)
		}

		rows := make([]map[string]any, 0, len(docs))
		for i, doc := range docs {
			id := doc.ID
			properties, err := s.documentProperties(doc)
			if err != nil {
				return fmt.Errorf("neo4j: decode metadata for %s: %w", id, err)
			}
			rows = append(rows, map[string]any{
				"id":         id,
				"properties": properties,
				"embedding":  embedding.Float32Vector(vectors[i]),
			})
		}

		// The count is deliberately not read. Run reads the RUN response
		// before returning, and the commit that ends the transaction function
		// discards any pending stream and reports whatever failure it carried,
		// so both halves of a write already reach this caller as an error.
		if err := s.write(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
			_, err := tx.Run(ctx, upsertCypher, map[string]any{
				"rows":              rows,
				"embeddingProperty": s.embeddingProperty,
			})
			return nil, err
		}); err != nil {
			return fmt.Errorf("neo4j: upsert: %w", err)
		}
	}
	return nil
}

// documentProperties assembles the complete owned property map written onto
// the upserted node. Replacing this map removes metadata keys that disappeared
// when a document with the same ID is reindexed.
func (s *Store) documentProperties(doc *document.Document) (map[string]any, error) {
	metadataValues, err := doc.Metadata.Values()
	if err != nil {
		return nil, err
	}
	props := make(map[string]any, len(doc.Metadata)+2)
	props[s.idProperty] = doc.ID
	props[s.textProperty] = doc.Text
	prefix := s.metadataPrefix + "."
	for k, v := range metadataValues {
		value, err := propertyValue(v)
		if err != nil {
			return nil, fmt.Errorf("neo4j: metadata %q: %w", k, err)
		}
		props[prefix+k] = value
	}
	return props, nil
}

// Search calls db.index.vector.queryNodes and returns matching
// documents above MinScore.
func (s *Store) Search(ctx context.Context, req *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	var docs []*vectorstore.SearchResult
	if err = req.Validate(); err != nil {
		return nil, fmt.Errorf("neo4j.Store.Search: %w", err)
	}
	if err = req.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, fmt.Errorf("neo4j.Store.Search: %w", err)
	}

	defer func() {
		if err == nil {
			err = response.ValidateFor(req)
		}
	}()

	vector, err := s.embeddingClient.EmbedText(ctx, req.Query)
	if err != nil {
		return nil, fmt.Errorf("neo4j: embed query: %w", err)
	}
	queryVec := embedding.Float32Vector(vector)

	wherePredicate, params, err := s.buildPredicate(req.Options.Filter)
	if err != nil {
		return nil, err
	}

	whereClause := "score >= $threshold"
	if wherePredicate != "" {
		whereClause = whereClause + " AND " + wherePredicate
	}

	cypher := fmt.Sprintf(
		"CALL db.index.vector.queryNodes($indexName, $k, $vec) YIELD node, score "+
			"WHERE %s RETURN node, score",
		whereClause,
	)

	if params == nil {
		params = make(map[string]any)
	}
	params["indexName"] = s.indexName
	params["k"] = req.Options.ResultLimit()
	params["vec"] = queryVec
	params["threshold"] = req.Options.MinScore

	session := s.session(ctx, neo4j.AccessModeRead)
	defer session.Close(ctx)

	var result any
	result, err = session.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		res, runErr := tx.Run(ctx, cypher, params)
		if runErr != nil {
			return nil, runErr
		}
		records, collectErr := res.Collect(ctx)
		if collectErr != nil {
			return nil, collectErr
		}
		out := make([]*vectorstore.SearchResult, 0, len(records))
		for _, rec := range records {
			match, convErr := s.recordToMatch(rec)
			if convErr != nil {
				return nil, convErr
			}
			out = append(out, match)
		}
		return out, nil
	})
	if err != nil {
		return nil, fmt.Errorf("neo4j: vector query: %w", err)
	}
	docs = result.([]*vectorstore.SearchResult)
	return &vectorstore.SearchResponse{Results: docs}, nil
}

func (s *Store) recordToMatch(rec *neo4j.Record) (*vectorstore.SearchResult, error) {
	nodeRaw, found := rec.Get("node")
	if !found {
		return nil, errors.New("neo4j: result record missing 'node' field")
	}
	node, ok := nodeRaw.(neo4j.Node)
	if !ok {
		return nil, fmt.Errorf("neo4j: unexpected node type %T", nodeRaw)
	}

	rawScore, found := rec.Get("score")
	if !found {
		return nil, errors.New("neo4j: result record missing 'score' field")
	}
	var score vectorstore.Score
	switch value := rawScore.(type) {
	case float64:
		score = vectorstore.ScoreFromValue(value)
	case float32:
		score = vectorstore.ScoreFromValue(float64(value))
	default:
		return nil, fmt.Errorf("neo4j: result score has type %T, want number", rawScore)
	}

	id, ok := node.Props[s.idProperty].(string)
	if !ok || id == "" {
		return nil, fmt.Errorf("neo4j: result node is missing string property %q", s.idProperty)
	}
	text, ok := node.Props[s.textProperty].(string)
	if !ok || text == "" {
		return nil, fmt.Errorf("neo4j: result node is missing string property %q", s.textProperty)
	}
	doc := &document.Document{ID: id, Text: text}

	metadataValues := s.metadataValues(node.Props)
	encodedMetadata, err := metadata.FromValues(metadataValues)
	if err != nil {
		return nil, fmt.Errorf("neo4j: convert metadata: %w", err)
	}
	doc.Metadata = encodedMetadata
	return &vectorstore.SearchResult{Document: doc, Score: score}, nil
}

func (s *Store) metadataValues(properties map[string]any) map[string]any {
	prefix := s.metadataPrefix + "."
	values := make(map[string]any)
	for key, value := range properties {
		if strings.HasPrefix(key, prefix) {
			values[strings.TrimPrefix(key, prefix)] = value
		}
	}
	if len(values) == 0 {
		return nil
	}
	return values
}

func (s *Store) DeleteWhere(ctx context.Context, expr filter.Predicate) (err error) {
	if expr == nil {
		return vectorstore.ErrMissingFilter
	}
	if err = expr.Validate(); err != nil {
		return fmt.Errorf("neo4j.Store.DeleteWhere: %w", err)
	}

	predicate, params, err := s.buildPredicate(expr)
	if err != nil {
		return err
	}
	if predicate == "" {
		return errors.New("neo4j: refusing to delete on empty filter")
	}

	cypher := fmt.Sprintf(
		"MATCH (node:`%s`) WHERE %s DETACH DELETE node",
		s.label, predicate,
	)

	return s.write(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		_, err := tx.Run(ctx, cypher, params)
		return nil, err
	})
}

// DeleteIDs removes nodes by document id — `MATCH ... WHERE n.<id> IN
// $ids DETACH DELETE n`. An empty slice is a no-op; unknown ids are
// silently ignored (idempotent). Implements [vectorstore.IDDeleter].
func (s *Store) DeleteIDs(ctx context.Context, ids []string) (err error) {
	if len(ids) == 0 {
		return nil
	}

	cypher := fmt.Sprintf(
		"MATCH (n:`%s`) WHERE n.`%s` IN $ids DETACH DELETE n",
		s.label, s.idProperty,
	)

	if err = s.write(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		_, runErr := tx.Run(ctx, cypher, map[string]any{"ids": ids})
		return nil, runErr
	}); err != nil {
		return fmt.Errorf("neo4j: delete by ids: %w", err)
	}
	return nil
}

// buildPredicate converts the optional filter into a Cypher WHERE
// fragment plus its parameter bindings. Returns ("", nil, nil) when
// filter is nil.
func (s *Store) buildPredicate(expr filter.Predicate) (string, map[string]any, error) {
	if expr == nil {
		return "", nil, nil
	}
	v := newVisitor("node", s.metadataPrefix)
	if err := expr.Accept(v); err != nil {
		return "", nil, fmt.Errorf("neo4j: convert filter: %w", err)
	}
	predicate, params := v.snapshot()
	return predicate, params, nil
}
