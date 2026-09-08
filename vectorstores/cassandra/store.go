package cassandra

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/gocql/gocql"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// Provider is the stable backend name for host-side attribution.
const Provider = "Cassandra"

// Exported defaults keep constructor behavior visible and overridable.
const (
	DefaultKeyspaceName    = "scope"
	DefaultTableName       = "vector_store"
	DefaultIDColumn        = "id"
	DefaultContentColumn   = "content"
	DefaultMetadataColumn  = "metadata"
	DefaultEmbeddingColumn = "embedding"
	DefaultSimilarity      = SimilarityCosine
)

// SimilarityFunction picks the function name used by the
// similarity_<func> built-in. The chosen value is recorded in the
// SAI index definition at creation time.
type SimilarityFunction string

const (
	// SimilarityCosine — cosine similarity. Default.
	SimilarityCosine SimilarityFunction = "cosine"

	// SimilarityDotProduct — dot product.
	SimilarityDotProduct SimilarityFunction = "dot_product"

	// SimilarityEuclidean — Euclidean (L2) distance, mapped to a
	// similarity score by Cassandra itself.
	SimilarityEuclidean SimilarityFunction = "euclidean"
)

func (s SimilarityFunction) Valid() bool {
	switch s {
	case SimilarityCosine, SimilarityDotProduct, SimilarityEuclidean:
		return true
	default:
		return false
	}
}

func (s SimilarityFunction) String() string { return string(s) }

// MetadataColumn declares a custom metadata column that the store
// indexes for filtering. Cassandra has no JSON-path operator, so each
// filterable metadata key must be a typed column on the table.
type MetadataColumn struct {
	// Name is the column identifier on the underlying table.
	Name string

	// CQLType is the column data type as written in CREATE TABLE
	// (e.g. "text", "int", "boolean", "double").
	CQLType string
}

// StoreConfig contains configuration options for the Cassandra vector
// store.
type StoreConfig struct {
	// Session is the gocql session. Required.
	Session *gocql.Session

	// KeyspaceName is the keyspace that holds the vector table.
	// Optional: defaults to [DefaultKeyspaceName].
	KeyspaceName string

	// TableName is the table that stores documents and their
	// embeddings. Optional: defaults to [DefaultTableName].
	TableName string

	// IDColumn / ContentColumn / EmbeddingColumn / MetadataColumn —
	// override the column names of the generated schema. Each
	// defaults to its respective Default* constant when empty.
	IDColumn        string
	ContentColumn   string
	EmbeddingColumn string

	// MetadataColumns enumerates the filterable metadata keys. Each
	// becomes a typed column on the table and (under
	// InitializeSchema) an SAI index. The optional [DocumentMetadata]
	// helpers may populate these from the Document.Metadata map.
	MetadataColumns []MetadataColumn

	// EmbeddingModel produces vectors for the documents. Required.
	EmbeddingModel embedding.Model

	// DocumentBatcher batches documents before insertion. Required.
	DocumentBatcher vectorstore.Batcher

	// Dimensions sets the VECTOR column width. When zero and
	// InitializeSchema is true, the store probes EmbeddingModel.
	Dimensions int

	// Similarity selects the vector similarity function. Optional:
	// defaults to [SimilarityCosine].
	Similarity SimilarityFunction

	// InitializeSchema, when true, creates the keyspace, table, and
	// SAI vector index if they don't already exist.
	InitializeSchema bool

	// KeyspaceReplication is the replication clause used when
	// InitializeSchema creates the keyspace — e.g.
	// "{'class': 'SimpleStrategy', 'replication_factor': 1}".
	// Optional: defaults to a single-replica SimpleStrategy.
	KeyspaceReplication string
}

func (s StoreConfig) Validate() error {
	s.applyDefaults()
	if s.Session == nil {
		return errors.New("cassandra: Session is required")
	}
	if lo.IsNil(s.EmbeddingModel) {
		return errors.New("cassandra: EmbeddingModel is required")
	}
	if lo.IsNil(s.DocumentBatcher) {
		return errors.New("cassandra: DocumentBatcher is required")
	}
	if s.Dimensions < 0 {
		return errors.New("cassandra: Dimensions must be >= 0")
	}
	if !s.Similarity.Valid() {
		return fmt.Errorf("cassandra: unsupported Similarity %q", s.Similarity)
	}

	return s.validateIdentifiers()
}

func (s StoreConfig) validateIdentifiers() error {
	if err := identifier(s.KeyspaceName).validate("KeyspaceName"); err != nil {
		return err
	}
	if err := identifier(s.TableName).validate("TableName"); err != nil {
		return err
	}
	if err := identifier(s.IDColumn).validate("IDColumn"); err != nil {
		return err
	}
	if err := identifier(s.ContentColumn).validate("ContentColumn"); err != nil {
		return err
	}
	if err := identifier(s.EmbeddingColumn).validate("EmbeddingColumn"); err != nil {
		return err
	}
	for _, m := range s.MetadataColumns {
		if m.Name == "" {
			return errors.New("cassandra: MetadataColumn.Name must not be empty")
		}
		if err := identifier(m.Name).validate("MetadataColumn.Name"); err != nil {
			return err
		}
		if m.CQLType == "" {
			return fmt.Errorf("cassandra: MetadataColumn %q must have a CQLType", m.Name)
		}
	}
	return nil
}

var (
	_ vectorstore.Indexer       = (*Store)(nil)
	_ vectorstore.Searcher      = (*Store)(nil)
	_ vectorstore.FilterDeleter = (*Store)(nil)
	_ vectorstore.IDDeleter     = (*Store)(nil)
)

// applyDefaults fills zero fields with documented defaults.
func (s *StoreConfig) applyDefaults() {
	s.KeyspaceName = cmp.Or(s.KeyspaceName, DefaultKeyspaceName)
	s.TableName = cmp.Or(s.TableName, DefaultTableName)
	s.IDColumn = cmp.Or(s.IDColumn, DefaultIDColumn)
	s.ContentColumn = cmp.Or(s.ContentColumn, DefaultContentColumn)
	s.EmbeddingColumn = cmp.Or(s.EmbeddingColumn, DefaultEmbeddingColumn)
	s.Similarity = cmp.Or(s.Similarity, DefaultSimilarity)
	if s.KeyspaceReplication == "" {
		s.KeyspaceReplication = "{'class': 'SimpleStrategy', 'replication_factor': 1}"
	}
}

// Store implements vector-store capabilities with Cassandra 5.0+ VECTOR
// columns and SAI indexes.
type Store struct {
	session         *gocql.Session
	keyspaceName    string
	tableName       string
	fullTable       string
	idColumn        string
	contentColumn   string
	embeddingColumn string
	metadataColumns []MetadataColumn
	embeddingClient embeddingclient.Client
	documentBatcher vectorstore.Batcher
	dimensions      int
	similarity      SimilarityFunction
}

// NewStore performs schema setup during construction, which is why it takes
// a context: a store returned before its keyspace table exists would fail on
// the first index rather than at wiring, where the misconfiguration actually
// is.
func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	config.applyDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}

	embeddingClient, err := embeddingclient.New(config.EmbeddingModel)
	if err != nil {
		return nil, fmt.Errorf("cassandra: create embedding client: %w", err)
	}

	store := &Store{
		session:         config.Session,
		keyspaceName:    config.KeyspaceName,
		tableName:       config.TableName,
		fullTable:       config.KeyspaceName + "." + config.TableName,
		idColumn:        config.IDColumn,
		contentColumn:   config.ContentColumn,
		embeddingColumn: config.EmbeddingColumn,
		metadataColumns: slices.Clone(config.MetadataColumns),
		embeddingClient: embeddingClient,
		documentBatcher: config.DocumentBatcher,
		dimensions:      config.Dimensions,
		similarity:      config.Similarity,
	}

	if err = store.initialize(ctx, config.InitializeSchema, config.KeyspaceReplication); err != nil {
		return nil, fmt.Errorf("cassandra: initialize store: %w", err)
	}
	return store, nil
}

// initialize resolves dimensions and provisions the schema when
// requested.
func (s *Store) initialize(ctx context.Context, initSchema bool, replication string) error {
	if !initSchema {
		return nil
	}
	if s.dimensions <= 0 {
		return errors.New("cassandra: Dimensions must be > 0")
	}

	stmts := []string{
		fmt.Sprintf("CREATE KEYSPACE IF NOT EXISTS %s WITH REPLICATION = %s",
			s.keyspaceName, replication),
	}

	var cols strings.Builder
	cols.WriteString(s.idColumn)
	cols.WriteString(" text PRIMARY KEY, ")
	cols.WriteString(s.contentColumn)
	cols.WriteString(" text, ")
	cols.WriteString(s.embeddingColumn)
	fmt.Fprintf(&cols, " vector<float, %d>", s.dimensions)
	for _, m := range s.metadataColumns {
		cols.WriteString(", ")
		cols.WriteString(m.Name)
		cols.WriteString(" ")
		cols.WriteString(m.CQLType)
	}
	stmts = append(stmts, fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS %s (%s)",
		s.fullTable, cols.String(),
	))

	// Vector SAI index for ANN search.
	stmts = append(stmts, fmt.Sprintf(
		"CREATE CUSTOM INDEX IF NOT EXISTS %s_vec_idx ON %s (%s) USING 'StorageAttachedIndex' "+
			"WITH OPTIONS = {'similarity_function': '%s'}",
		s.tableName, s.fullTable, s.embeddingColumn, s.similarity,
	))

	// SAI index per metadata column so the visitor's WHERE
	// predicates can run without ALLOW FILTERING.
	for _, m := range s.metadataColumns {
		stmts = append(stmts, fmt.Sprintf(
			"CREATE CUSTOM INDEX IF NOT EXISTS %s_%s_idx ON %s (%s) USING 'StorageAttachedIndex'",
			s.tableName, m.Name, s.fullTable, m.Name,
		))
	}

	for _, stmt := range stmts {
		if err := s.session.Query(stmt).WithContext(ctx).Exec(); err != nil {
			return fmt.Errorf("execute %q: %w", firstLine(stmt), err)
		}
	}
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i > 0 {
		return s[:i]
	}
	return s
}

// Index embeds documents and inserts them.
func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) (err error) {
	if validateErr := request.Validate(); validateErr != nil {
		return fmt.Errorf("cassandra.Store.Index: %w", validateErr)
	}

	var batches []*vectorstore.IndexRequest
	batches, err = request.Batch(ctx, s.documentBatcher)
	if err != nil {
		return fmt.Errorf("cassandra: batch documents: %w", err)
	}

	for _, batch := range batches {
		docs := batch.Documents
		texts, err := batch.Texts()
		if err != nil {
			return fmt.Errorf("vectorstore: project document text: %w", err)
		}
		vectors, err := s.embeddingClient.EmbedTexts(ctx, texts)
		if err != nil {
			return fmt.Errorf("cassandra: embed documents: %w", err)
		}

		for i, doc := range docs {
			if err := s.insertOne(ctx, doc.ID, doc, vectors[i]); err != nil {
				return err
			}
		}
	}
	return nil
}

// insertOne issues an UPSERT (INSERT in Cassandra always upserts on
// primary key). The vector is inlined as a CQL literal because the
// gocql v1.x driver doesn't support typed vector binding.
func (s *Store) insertOne(ctx context.Context, id string, doc *document.Document, vec []float64) error {
	vectorJSON, err := json.Marshal(embedding.Float32Vector(vec))
	if err != nil {
		return fmt.Errorf("cassandra: marshal vector for %s: %w", id, err)
	}
	columns := []string{s.idColumn, s.contentColumn, s.embeddingColumn}
	placeholders := []string{"?", "?", string(vectorJSON)}
	args := []any{id, doc.Text}

	for _, m := range s.metadataColumns {
		val, ok, err := doc.Metadata.Decode[any](m.Name)
		if err != nil {
			return fmt.Errorf("cassandra: decode metadata %s: %w", m.Name, err)
		}
		if ok {
			columns = append(columns, m.Name)
			placeholders = append(placeholders, "?")
			args = append(args, val)
		}
	}

	stmt := fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s)",
		s.fullTable, strings.Join(columns, ", "), strings.Join(placeholders, ", "),
	)
	if err := s.session.Query(stmt, args...).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("cassandra: insert %s: %w", id, err)
	}
	return nil
}

// Search runs an ANN query using the configured similarity function.
func (s *Store) Search(ctx context.Context, req *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	var docs []*vectorstore.SearchResult
	if err = req.Validate(); err != nil {
		return nil, fmt.Errorf("cassandra.Store.Search: %w", err)
	}
	if err = req.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, fmt.Errorf("cassandra.Store.Search: %w", err)
	}

	defer func() {
		if err == nil {
			err = response.ValidateFor(req)
		}
	}()

	vector, err := s.embeddingClient.EmbedText(ctx, req.Query)
	if err != nil {
		return nil, fmt.Errorf("cassandra: embed query: %w", err)
	}
	vectorJSON, err := json.Marshal(embedding.Float32Vector(vector))
	if err != nil {
		return nil, fmt.Errorf("cassandra: marshal query vector: %w", err)
	}
	vecLiteral := string(vectorJSON)

	wherePredicate, whereArgs, err := s.buildFilter(req.Options.Filter)
	if err != nil {
		return nil, err
	}

	wherePart := ""
	if wherePredicate != "" {
		wherePart = " WHERE " + wherePredicate
	}

	columns := []string{
		s.idColumn,
		s.contentColumn,
		fmt.Sprintf("similarity_%s(%s, %s) AS score", s.similarity, s.embeddingColumn, vecLiteral),
	}
	for _, m := range s.metadataColumns {
		columns = append(columns, m.Name)
	}

	stmt := fmt.Sprintf(
		"SELECT %s FROM %s%s ORDER BY %s ANN OF %s LIMIT %d",
		strings.Join(columns, ", "), s.fullTable, wherePart,
		s.embeddingColumn, vecLiteral, req.Options.ResultLimit(),
	)

	iterator := queryIterator{value: s.session.Query(stmt, whereArgs...).WithContext(ctx).Iter()}
	defer func() {
		if closeErr := iterator.close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("cassandra: close query iterator for %s: %w", s.fullTable, closeErr))
		}
	}()

	docs = make([]*vectorstore.SearchResult, 0, req.Options.ResultLimit())
	scanDestinations := s.scanDestinations()
	for iterator.scan(scanDestinations...) {
		match, err := s.searchResultFromScan(scanDestinations, req.Options.MinScore)
		if err != nil {
			return nil, err
		}
		if match == nil {
			continue
		}
		docs = append(docs, match)
	}
	if closeErr := iterator.close(); closeErr != nil {
		return nil, fmt.Errorf("cassandra: close query iterator for %s: %w", s.fullTable, closeErr)
	}
	return &vectorstore.SearchResponse{Results: docs}, nil
}

type queryIterator struct {
	value  *gocql.Iter
	closed bool
}

const (
	scanDocumentIDIndex = iota
	scanContentIndex
	scanScoreIndex
	scanMetadataOffset
)

func (q *queryIterator) scan(destinations ...any) bool {
	return !q.closed && q.value.Scan(destinations...)
}

func (q *queryIterator) close() error {
	if q.closed {
		return nil
	}
	q.closed = true
	return q.value.Close()
}

// gocql.Scan needs one pointer for every selected column, including the
// configuration-dependent metadata tail.
func (s *Store) scanDestinations() []any {
	destinations := []any{new(string), new(string), new(float32)}
	for range s.metadataColumns {
		destinations = append(destinations, new(any))
	}
	return destinations
}

func (s *Store) searchResultFromScan(destinations []any, minScore vectorstore.Score) (*vectorstore.SearchResult, error) {
	id := *destinations[scanDocumentIDIndex].(*string)
	text := *destinations[scanContentIndex].(*string)
	score := vectorstore.ScoreFromValue(float64(*destinations[scanScoreIndex].(*float32)))
	if score < minScore {
		return nil, nil
	}
	if id == "" {
		return nil, errors.New("cassandra: search result is missing document ID")
	}
	if text == "" {
		return nil, errors.New("cassandra: search result is missing document text")
	}

	doc := &document.Document{ID: id, Text: text}
	if len(s.metadataColumns) > 0 {
		meta := make(map[string]any, len(s.metadataColumns))
		for index, column := range s.metadataColumns {
			value := *(destinations[scanMetadataOffset+index].(*any))
			if value != nil {
				meta[column.Name] = value
			}
		}
		if len(meta) > 0 {
			var err error
			doc.Metadata, err = metadata.FromValues(meta)
			if err != nil {
				return nil, fmt.Errorf("cassandra: convert metadata: %w", err)
			}
		}
	}
	return &vectorstore.SearchResult{Document: doc, Score: score}, nil
}

func (s *Store) DeleteWhere(ctx context.Context, expr filter.Predicate) (err error) {
	if expr == nil {
		return vectorstore.ErrMissingFilter
	}
	if err = expr.Validate(); err != nil {
		return fmt.Errorf("cassandra.Store.DeleteWhere: %w", err)
	}

	predicate, args, err := s.buildFilter(expr)
	if err != nil {
		return err
	}
	if predicate == "" {
		return errors.New("cassandra: refusing to delete on empty filter")
	}

	selectStmt := fmt.Sprintf("SELECT %s FROM %s WHERE %s", s.idColumn, s.fullTable, predicate)
	iterator := queryIterator{value: s.session.Query(selectStmt, args...).WithContext(ctx).Iter()}
	defer func() {
		if closeErr := iterator.close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("cassandra: close ID iterator: %w", closeErr))
		}
	}()

	var ids []string
	var id string
	for iterator.scan(&id) {
		ids = append(ids, id)
	}
	if closeErr := iterator.close(); closeErr != nil {
		return fmt.Errorf("cassandra: close ID iterator: %w", closeErr)
	}
	if len(ids) == 0 {
		return nil
	}

	deleteStmt := fmt.Sprintf("DELETE FROM %s WHERE %s = ?", s.fullTable, s.idColumn)
	for _, id := range ids {
		if err := s.session.Query(deleteStmt, id).WithContext(ctx).Exec(); err != nil {
			return fmt.Errorf("cassandra: delete %s: %w", id, err)
		}
	}
	return nil
}

// DeleteIDs removes rows by primary key. Because the id column is the
// partition key, CQL allows a single DELETE with an IN list over it:
// `DELETE FROM <table> WHERE <idCol> IN (?, ?, ...)`. An empty slice is a
// no-op; unknown ids are silently ignored (idempotent). Implements
// [vectorstore.IDDeleter].
func (s *Store) DeleteIDs(ctx context.Context, ids []string) (err error) {
	if len(ids) == 0 {
		return nil
	}

	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}

	stmt := fmt.Sprintf(
		"DELETE FROM %s WHERE %s IN (%s)",
		s.fullTable, s.idColumn, strings.Join(placeholders, ", "),
	)
	if err = s.session.Query(stmt, args...).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("cassandra: delete by ids from %s: %w", s.fullTable, err)
	}
	return nil
}

func (s *Store) buildFilter(expr filter.Predicate) (string, []any, error) {
	if expr == nil {
		return "", nil, nil
	}
	v := newVisitor()
	if err := expr.Accept(v); err != nil {
		return "", nil, fmt.Errorf("cassandra: convert filter: %w", err)
	}
	predicate, args := v.snapshot()
	return predicate, args, nil
}

func (s *Store) Close() error { return nil }
