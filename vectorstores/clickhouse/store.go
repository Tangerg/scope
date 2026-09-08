package clickhouse

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// Provider is the stable backend name for host-side attribution.
const Provider = "ClickHouse"

// Exported defaults keep constructor behavior visible and overridable.
const (
	DefaultTableName       = "vector_store"
	DefaultIDColumn        = "id"
	DefaultContentColumn   = "content"
	DefaultMetadataColumn  = "metadata"
	DefaultEmbeddingColumn = "embedding"
	DefaultDistanceMetric  = DistanceCosine
)

// DistanceMetric selects the distance function ClickHouse uses to
// rank rows.
type DistanceMetric string

const (
	// DistanceCosine uses cosineDistance(a, b) — returns 1 - cosine
	// similarity, range [0, 2].
	DistanceCosine DistanceMetric = "cosine"

	// DistanceL2 uses L2Distance(a, b) — Euclidean distance,
	// range [0, ∞).
	DistanceL2 DistanceMetric = "l2"
)

func (d DistanceMetric) Valid() bool {
	return d == DistanceCosine || d == DistanceL2
}

func (d DistanceMetric) String() string { return string(d) }

func (d DistanceMetric) function() string {
	switch d {
	case DistanceL2:
		return "L2Distance"
	case DistanceCosine:
		fallthrough
	default:
		return "cosineDistance"
	}
}

func (d DistanceMetric) score(distance float64) vectorstore.Score {
	switch d {
	case DistanceL2:
		return vectorstore.ScoreFromDistance(distance)
	case DistanceCosine:
		fallthrough
	default:
		return vectorstore.ScoreFromCosineDistance(distance)
	}
}

// Connection is the ClickHouse surface the store uses: a statement executor, a
// row reader, and the typed batch insert. A clickhouse-go v2 [driver.Conn]
// satisfies it. Naming only the three operations keeps every statement the
// store issues observable without a live server.
type Connection interface {
	Exec(ctx context.Context, query string, args ...any) error
	Query(ctx context.Context, query string, args ...any) (driver.Rows, error)
	PrepareBatch(ctx context.Context, query string, opts ...driver.PrepareBatchOption) (driver.Batch, error)
}

// StoreConfig contains configuration options for the ClickHouse
// vector store. The default schema uses `Map(String, String)` for
// metadata to keep the visitor's column-subscript syntax simple;
// callers needing typed metadata columns should manage the schema
// themselves and set InitializeSchema=false.
type StoreConfig struct {
	// Conn is the clickhouse-go v2 driver connection. Required.
	Conn Connection

	// DatabaseName is the optional database prefix; empty uses the
	// connection's current database.
	DatabaseName string

	TableName       string
	IDColumn        string
	ContentColumn   string
	MetadataColumn  string
	EmbeddingColumn string

	EmbeddingModel  embedding.Model
	DocumentBatcher vectorstore.Batcher

	Dimensions       int
	DistanceMetric   DistanceMetric
	InitializeSchema bool
}

func (s StoreConfig) Validate() error {
	s.applyDefaults()
	if lo.IsNil(s.Conn) {
		return errors.New("clickhouse: Conn is required")
	}
	if lo.IsNil(s.EmbeddingModel) {
		return errors.New("clickhouse: EmbeddingModel is required")
	}
	if lo.IsNil(s.DocumentBatcher) {
		return errors.New("clickhouse: DocumentBatcher is required")
	}
	if s.Dimensions < 0 {
		return errors.New("clickhouse: Dimensions must be >= 0")
	}
	if !s.DistanceMetric.Valid() {
		return fmt.Errorf("clickhouse: unsupported DistanceMetric %q", s.DistanceMetric)
	}
	return s.validateIdentifiers()
}

func (s StoreConfig) validateIdentifiers() error {
	if s.DatabaseName != "" {
		if err := identifier(s.DatabaseName).validate("DatabaseName"); err != nil {
			return err
		}
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
	if err := identifier(s.MetadataColumn).validate("MetadataColumn"); err != nil {
		return err
	}
	return identifier(s.EmbeddingColumn).validate("EmbeddingColumn")
}

// applyDefaults fills zero fields with documented defaults.
func (s *StoreConfig) applyDefaults() {
	s.TableName = cmp.Or(s.TableName, DefaultTableName)
	s.IDColumn = cmp.Or(s.IDColumn, DefaultIDColumn)
	s.ContentColumn = cmp.Or(s.ContentColumn, DefaultContentColumn)
	s.MetadataColumn = cmp.Or(s.MetadataColumn, DefaultMetadataColumn)
	s.EmbeddingColumn = cmp.Or(s.EmbeddingColumn, DefaultEmbeddingColumn)
	s.DistanceMetric = cmp.Or(s.DistanceMetric, DefaultDistanceMetric)
}

var (
	_ vectorstore.Indexer       = (*Store)(nil)
	_ vectorstore.Searcher      = (*Store)(nil)
	_ vectorstore.FilterDeleter = (*Store)(nil)
	_ vectorstore.IDDeleter     = (*Store)(nil)
)

// Store implements vector-store capabilities with ClickHouse.
type Store struct {
	conn            Connection
	databaseName    string
	tableName       string
	fullTable       string
	idColumn        string
	contentColumn   string
	metadataColumn  string
	embeddingColumn string
	embeddingClient embeddingclient.Client
	documentBatcher vectorstore.Batcher
	dimensions      int
	distanceMetric  DistanceMetric
}

// NewStore performs schema setup during construction, which is why it takes
// a context: a store returned before its table and index exist would fail
// on the first index rather than at wiring, where the misconfiguration
// actually is.
func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	config.applyDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}
	embeddingClient, err := embeddingclient.New(config.EmbeddingModel)
	if err != nil {
		return nil, fmt.Errorf("clickhouse: create embedding client: %w", err)
	}
	fullTable := config.TableName
	if config.DatabaseName != "" {
		fullTable = config.DatabaseName + "." + config.TableName
	}
	store := &Store{
		conn:            config.Conn,
		databaseName:    config.DatabaseName,
		tableName:       config.TableName,
		fullTable:       fullTable,
		idColumn:        config.IDColumn,
		contentColumn:   config.ContentColumn,
		metadataColumn:  config.MetadataColumn,
		embeddingColumn: config.EmbeddingColumn,
		embeddingClient: embeddingClient,
		documentBatcher: config.DocumentBatcher,
		dimensions:      config.Dimensions,
		distanceMetric:  config.DistanceMetric,
	}
	if err = store.initialize(ctx, config.InitializeSchema); err != nil {
		return nil, fmt.Errorf("clickhouse: initialize store: %w", err)
	}
	return store, nil
}

func (s *Store) initialize(ctx context.Context, initSchema bool) error {
	if !initSchema {
		return nil
	}
	if s.dimensions <= 0 {
		return errors.New("clickhouse: Dimensions must be > 0")
	}

	stmt := fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %s (
			%s String,
			%s String,
			%s Map(String, String),
			%s Array(Float32),
			CONSTRAINT vec_len CHECK length(%s) = %d,
			INDEX vec_idx %s TYPE vector_similarity('hnsw', '%s', %d) GRANULARITY 1
		) ENGINE = MergeTree() ORDER BY (%s)`,
		s.fullTable,
		s.idColumn,
		s.contentColumn,
		s.metadataColumn,
		s.embeddingColumn,
		s.embeddingColumn, s.dimensions,
		s.embeddingColumn, s.distanceMetric.function(), s.dimensions,
		s.idColumn,
	)
	if err := s.conn.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("create table %s: %w", s.fullTable, err)
	}
	return nil
}

// Index embeds documents and inserts them as a single batch.
func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) (err error) {
	if validateErr := request.Validate(); validateErr != nil {
		return fmt.Errorf("clickhouse.Store.Index: %w", validateErr)
	}

	var batches []*vectorstore.IndexRequest
	batches, err = request.Batch(ctx, s.documentBatcher)
	if err != nil {
		return fmt.Errorf("clickhouse: batch documents: %w", err)
	}

	insertSQL := fmt.Sprintf(
		"INSERT INTO %s (%s, %s, %s, %s)",
		s.fullTable, s.idColumn, s.contentColumn, s.metadataColumn, s.embeddingColumn,
	)

	for _, batch := range batches {
		docs := batch.Documents
		texts, err := batch.Texts()
		if err != nil {
			return fmt.Errorf("vectorstore: project document text: %w", err)
		}
		vectors, err := s.embeddingClient.EmbedTexts(ctx, texts)
		if err != nil {
			return fmt.Errorf("clickhouse: embed documents: %w", err)
		}

		batch, err := s.conn.PrepareBatch(ctx, insertSQL)
		if err != nil {
			return fmt.Errorf("clickhouse: prepare batch: %w", err)
		}

		appendErr := func() error {
			for i, doc := range docs {
				id := doc.ID
				meta, err := metadataAsStringMap(doc.Metadata)
				if err != nil {
					return fmt.Errorf("metadata for %s: %w", id, err)
				}
				vec32 := embedding.Float32Vector(vectors[i])
				if err := batch.Append(id, doc.Text, meta, vec32); err != nil {
					return fmt.Errorf("append %s: %w", id, err)
				}
			}
			return batch.Send()
		}()
		if appendErr != nil {
			return appendErr
		}
	}
	return nil
}

// Search runs an ANN search using the configured distance function.
func (s *Store) Search(ctx context.Context, req *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	var docs []*vectorstore.SearchResult
	if err = req.Validate(); err != nil {
		return nil, fmt.Errorf("clickhouse.Store.Search: %w", err)
	}
	if err = req.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, fmt.Errorf("clickhouse.Store.Search: %w", err)
	}

	defer func() {
		if err == nil {
			err = response.ValidateFor(req)
		}
	}()

	vector, err := s.embeddingClient.EmbedText(ctx, req.Query)
	if err != nil {
		return nil, fmt.Errorf("clickhouse: embed query: %w", err)
	}
	queryVec := embedding.Float32Vector(vector)

	wherePredicate, whereArgs, err := s.buildFilter(req.Options.Filter)
	if err != nil {
		return nil, err
	}
	wherePart := ""
	if wherePredicate != "" {
		wherePart = " AND " + wherePredicate
	}

	stmt := fmt.Sprintf(
		`SELECT %s, %s, %s, %s(%s, ?) AS distance FROM %s WHERE 1=1%s ORDER BY distance ASC LIMIT ?`,
		s.idColumn, s.contentColumn, s.metadataColumn,
		s.distanceMetric.function(), s.embeddingColumn,
		s.fullTable, wherePart,
	)

	args := []any{queryVec}
	args = append(args, whereArgs...)
	args = append(args, req.Options.ResultLimit())

	rows, err := s.conn.Query(ctx, stmt, args...)
	if err != nil {
		return nil, fmt.Errorf("clickhouse: query %s: %w", s.fullTable, err)
	}
	defer rows.Close()

	docs = make([]*vectorstore.SearchResult, 0, req.Options.ResultLimit())
	for rows.Next() {
		var (
			id       string
			content  string
			metaRaw  map[string]string
			distance float64
		)
		if err := rows.Scan(&id, &content, &metaRaw, &distance); err != nil {
			return nil, fmt.Errorf("clickhouse: scan row: %w", err)
		}
		score := s.distanceMetric.score(distance)
		if score < req.Options.MinScore {
			continue
		}
		if id == "" {
			return nil, errors.New("clickhouse: search result is missing document ID")
		}
		if content == "" {
			return nil, errors.New("clickhouse: search result is missing document text")
		}
		metadata, err := stringMapToMetadata(metaRaw)
		if err != nil {
			return nil, fmt.Errorf("clickhouse: convert metadata: %w", err)
		}
		docs = append(docs, &vectorstore.SearchResult{
			Document: &document.Document{ID: id, Text: content, Metadata: metadata},
			Score:    score,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("clickhouse: read rows: %w", err)
	}
	return &vectorstore.SearchResponse{Results: docs}, nil
}

func (s *Store) DeleteWhere(ctx context.Context, expr filter.Predicate) (err error) {
	if expr == nil {
		return vectorstore.ErrMissingFilter
	}
	if err = expr.Validate(); err != nil {
		return fmt.Errorf("clickhouse.Store.DeleteWhere: %w", err)
	}

	var (
		predicate string
		args      []any
	)
	predicate, args, err = s.buildFilter(expr)
	if err != nil {
		return err
	}
	if predicate == "" {
		return errors.New("clickhouse: refusing to delete on empty filter")
	}
	return s.deleteMatching(ctx, predicate, args...)
}

// DeleteIDs removes rows by primary key, matching the form DeleteWhere uses. An
// empty slice is a no-op; unknown ids are silently ignored. Implements
// [vectorstore.IDDeleter].
func (s *Store) DeleteIDs(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}

	placeholders := strings.Repeat("?, ", len(ids)-1) + "?"
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return s.deleteMatching(ctx, fmt.Sprintf("%s IN (%s)", s.idColumn, placeholders), args...)
}

// lightweightDeletesWaitForReplicas makes DELETE FROM wait for every replica to
// mark the rows deleted. It restates ClickHouse's own default for
// lightweight_deletes_sync so a connection cannot lower it under the store.
const lightweightDeletesWaitForReplicas = 2

// deleteMatching removes every row predicate selects and does not return until
// those rows have stopped being retrievable.
//
// ClickHouse offers two deletions and only one of them can carry the
// [vectorstore.FilterDeleter] and [vectorstore.IDDeleter] contracts. `ALTER
// TABLE ... DELETE` is a mutation, and a mutation query returns as soon as its
// entry is recorded while the work runs asynchronously in the background — a
// successful call proves the delete was queued, not that it happened. A
// lightweight `DELETE FROM` instead waits until marking the rows as deleted is
// complete, so it is the statement both delete paths issue, through one owner
// that pins the wait rather than inheriting it from the caller's connection.
func (s *Store) deleteMatching(ctx context.Context, predicate string, args ...any) error {
	ctx = clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{
		"lightweight_deletes_sync": lightweightDeletesWaitForReplicas,
	}))
	stmt := fmt.Sprintf("DELETE FROM %s WHERE %s", s.fullTable, predicate)
	if err := s.conn.Exec(ctx, stmt, args...); err != nil {
		return fmt.Errorf("clickhouse: delete from %s: %w", s.fullTable, err)
	}
	return nil
}

func (s *Store) buildFilter(expr filter.Predicate) (string, []any, error) {
	if expr == nil {
		return "", nil, nil
	}
	v := newVisitor(s.metadataColumn)
	if err := expr.Accept(v); err != nil {
		return "", nil, fmt.Errorf("clickhouse: convert filter: %w", err)
	}
	predicate, args := v.snapshot()
	return predicate, args, nil
}

func (s *Store) Close() error { return nil }

// metadataAsStringMap carries each metadata value into the
// `Map(String, String)` column as its JSON text.
//
// [metadata.Map] is a map of JSON values, so the JSON text is the exact value
// and a Map(String, String) holds it verbatim. Going through Values() and
// stringifying the decoded scalars instead cost the type: a document indexed
// with year 2020 came back with the string "2020", so Decode[int] failed on a
// document this store had accepted, and a nil value became "" — the same text
// as an empty string, which the filter AST reads as a different value. This
// pair is a bijection, so a document reads back as it was written.
//
// A string value therefore arrives quoted, which is why the filter visitor
// compares against the JSON encoding of a literal rather than its bare text.
func metadataAsStringMap(m metadata.Map) (map[string]string, error) {
	if err := m.Validate(); err != nil {
		return nil, fmt.Errorf("clickhouse: encode metadata: %w", err)
	}
	out := make(map[string]string, len(m))
	for key, raw := range m {
		out[key] = string(raw)
	}
	return out, nil
}

func stringMapToMetadata(m map[string]string) (metadata.Map, error) {
	if len(m) == 0 {
		return nil, nil
	}
	out := make(metadata.Map, len(m))
	for key, text := range m {
		out[key] = json.RawMessage(text)
	}
	if err := out.Validate(); err != nil {
		return nil, fmt.Errorf("clickhouse: decode metadata: %w", err)
	}
	return out, nil
}
