package clickhouse

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
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

// StoreConfig binds the host-owned native ClickHouse database/sql handle.
// The server must support native transactions; all concurrent writers must
// use transactions to participate in its snapshots.
type StoreConfig struct {
	// DB uses clickhouse-go v2 with the native TCP protocol. The host owns it.
	DB *sql.DB

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
	if lo.IsNil(s.DB) {
		return errors.New("clickhouse: DB is required")
	}
	if lo.IsNil(s.EmbeddingModel) {
		return errors.New("clickhouse: EmbeddingModel is required")
	}
	if lo.IsNil(s.DocumentBatcher) {
		return errors.New("clickhouse: DocumentBatcher is required")
	}
	if s.Dimensions < 0 || (s.InitializeSchema && s.Dimensions == 0) {
		return errors.New("clickhouse: Dimensions must be non-negative and positive for schema creation")
	}
	if !s.DistanceMetric.Valid() {
		return fmt.Errorf("clickhouse: unsupported DistanceMetric %q", s.DistanceMetric)
	}

	names := []string{s.IDColumn, s.ContentColumn, s.MetadataColumn, s.EmbeddingColumn}
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if _, exists := seen[name]; exists {
			return fmt.Errorf("clickhouse: columns must have distinct names: %q", name)
		}
		seen[name] = struct{}{}
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

// Store implements vector-store capabilities with native transaction snapshots.
type Store struct {
	db              *sql.DB
	databaseName    string
	tableName       string
	fullTable       string
	idColumn        string
	contentColumn   string
	metadataColumn  string
	embeddingColumn string
	embeddingClient embeddingclient.Client
	documentBatcher vectorstore.Batcher
	distanceMetric  DistanceMetric
}

// NewStore verifies native transaction support and the strict current schema.
// An omitted database is resolved once; every operation uses its qualified table.
func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	config.applyDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}
	embeddingClient, err := embeddingclient.New(config.EmbeddingModel)
	if err != nil {
		return nil, fmt.Errorf("clickhouse: create embedding client: %w", err)
	}
	transaction, err := newNativeTransaction(ctx, config.DB)
	if err != nil {
		return nil, fmt.Errorf("clickhouse: native transactions are required: %w", err)
	}
	if err := transaction.Close(); err != nil {
		return nil, err
	}
	if config.DatabaseName == "" {
		if err := config.DB.QueryRowContext(nativeStatementContext(ctx, false), "SELECT currentDatabase()").Scan(&config.DatabaseName); err != nil {
			return nil, fmt.Errorf("clickhouse: resolve database: %w", err)
		}
		if err := identifier(config.DatabaseName).validate("DatabaseName"); err != nil {
			return nil, err
		}
	}
	store := &Store{
		db: config.DB, databaseName: config.DatabaseName, tableName: config.TableName, fullTable: config.DatabaseName + "." + config.TableName,
		idColumn: config.IDColumn, contentColumn: config.ContentColumn, metadataColumn: config.MetadataColumn, embeddingColumn: config.EmbeddingColumn,
		embeddingClient: embeddingClient, documentBatcher: config.DocumentBatcher, distanceMetric: config.DistanceMetric,
	}
	if config.InitializeSchema {
		if err := store.initialize(ctx, config.Dimensions); err != nil {
			return nil, err
		}
	}
	if err := store.validateTable(ctx); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) initialize(ctx context.Context, dimensions int) error {
	statement := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
  %s String, %s String, %s String, %s Array(Float32),
  CONSTRAINT vec_len CHECK length(%s) = %d,
  CONSTRAINT vec_finite CHECK arrayAll(x -> isFinite(x), %s)
 ) ENGINE = ReplacingMergeTree() ORDER BY (%s)`, s.fullTable, s.idColumn, s.contentColumn, s.metadataColumn, s.embeddingColumn, s.embeddingColumn, dimensions, s.embeddingColumn, s.idColumn)
	if _, err := s.db.ExecContext(nativeStatementContext(ctx, false), statement); err != nil {
		return fmt.Errorf("clickhouse: create table %s: %w", s.fullTable, err)
	}
	return nil
}

// Index embeds and sends each batch through the driver's typed insertion API.
// implicit_transaction gives its native INSERT one publication owner; the
// database/sql transaction only flushes the driver's buffered batch.
func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) error {
	if err := request.Validate(); err != nil {
		return fmt.Errorf("clickhouse.Store.Index: %w", err)
	}
	for index, doc := range request.Documents {
		if doc.Media != nil {
			return fmt.Errorf("clickhouse.Store.Index: %w: documents[%d] contains unsupported media", vectorstore.ErrInvalidDocument, index)
		}
	}
	batches, err := request.Batch(ctx, s.documentBatcher)
	if err != nil {
		return fmt.Errorf("clickhouse: batch documents: %w", err)
	}
	for _, batch := range batches {
		texts, err := batch.Texts()
		if err != nil {
			return err
		}
		vectors, err := s.embeddingClient.EmbedTexts(ctx, texts)
		if err != nil {
			return fmt.Errorf("clickhouse: embed documents: %w", err)
		}
		if err := s.insertBatch(ctx, batch.Documents, texts, vectors); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) insertBatch(ctx context.Context, docs []*document.Document, texts []string, vectors [][]float64) (err error) {
	ctx = nativeStatementContext(ctx, true)
	batch, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("clickhouse: open insert batch: %w", err)
	}
	defer func() {
		if rollbackErr := batch.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("clickhouse: discard insert batch: %w", rollbackErr))
		}
	}()
	statement, err := batch.PrepareContext(ctx, fmt.Sprintf("INSERT INTO %s (%s, %s, %s, %s)", s.fullTable, s.idColumn, s.contentColumn, s.metadataColumn, s.embeddingColumn))
	if err != nil {
		return fmt.Errorf("clickhouse: prepare insert batch: %w", err)
	}
	defer func() {
		if closeErr := statement.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("clickhouse: close insert batch: %w", closeErr))
		}
	}()
	for index, doc := range docs {
		encoded, err := doc.Metadata.MarshalJSON()
		if err != nil {
			return fmt.Errorf("clickhouse: encode metadata for %q: %w", doc.ID, err)
		}
		if _, err := statement.ExecContext(ctx, doc.ID, texts[index], string(encoded), embedding.Float32Vector(vectors[index])); err != nil {
			return fmt.Errorf("clickhouse: append %q: %w", doc.ID, err)
		}
	}
	if err := batch.Commit(); err != nil {
		return fmt.Errorf("clickhouse: send insert batch: %w", err)
	}
	return nil
}

// Search uses native distances and exact ID ordering. Core evaluates all filters.
func (s *Store) Search(ctx context.Context, request *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	if err = request.Validate(); err != nil {
		return nil, fmt.Errorf("clickhouse.Store.Search: %w", err)
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
	if request.Options.Filter == nil {
		return s.search(ctx, request, nil)
	}
	transaction, err := newNativeTransaction(ctx, s.db)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, transaction.Close()) }()
	matches, err := s.preflightFilter(transaction, request.Options.Filter)
	if err != nil {
		return nil, err
	}
	if !matches {
		response = &vectorstore.SearchResponse{}
	}
	if matches {
		response, err = s.search(ctx, request, transaction)
		if err != nil {
			return nil, err
		}
	}
	if err := transaction.Commit(); err != nil {
		return nil, err
	}
	return response, nil
}

func (s *Store) search(ctx context.Context, request *vectorstore.SearchRequest, transaction *nativeTransaction) (response *vectorstore.SearchResponse, err error) {
	vector, err := s.embeddingClient.EmbedText(ctx, request.Query)
	if err != nil {
		return nil, fmt.Errorf("clickhouse: embed query: %w", err)
	}
	statement := fmt.Sprintf("SELECT %s, %s, %s, toFloat64(%s(%s, {scope_vector:Array(Float32)})) AS distance FROM %s FINAL ORDER BY distance, %s", s.idColumn, s.contentColumn, s.metadataColumn, s.distanceMetric.function(), s.embeddingColumn, s.fullTable, s.idColumn)
	args := []any{sql.Named("scope_vector", embedding.Float32Vector(vector))}
	if request.Options.Filter == nil {
		statement += " LIMIT {scope_limit:UInt64}"
		args = append(args, sql.Named("scope_limit", request.Options.ResultLimit()))
	}
	var rows *sql.Rows
	if transaction == nil {
		rows, err = s.db.QueryContext(nativeStatementContext(ctx, true), statement, args...)
	} else {
		rows, err = transaction.Query(statement, args...)
	}
	if err != nil {
		return nil, fmt.Errorf("clickhouse: query %s: %w", s.fullTable, err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("clickhouse: close search rows: %w", closeErr))
		}
		if err != nil {
			response = nil
		}
	}()
	results := make([]*vectorstore.SearchResult, 0, request.Options.ResultLimit())
	for rows.Next() {
		doc := &document.Document{}
		var encoded string
		var distance float64
		if err := rows.Scan(&doc.ID, &doc.Text, &encoded, &distance); err != nil {
			return nil, fmt.Errorf("clickhouse: scan search row: %w", err)
		}
		if err := doc.Metadata.UnmarshalJSON([]byte(encoded)); err != nil {
			return nil, fmt.Errorf("clickhouse: decode metadata for %q: %w", doc.ID, err)
		}
		if request.Options.Filter != nil {
			values, err := doc.Metadata.Values()
			if err != nil {
				return nil, err
			}
			matches, err := filter.Match(request.Options.Filter, values)
			if err != nil {
				return nil, fmt.Errorf("clickhouse: evaluate metadata for %q: %w", doc.ID, err)
			}
			if !matches {
				continue
			}
		}
		result, err := vectorstore.NewSearchResult(doc, s.distanceMetric.score(distance))
		if err != nil {
			return nil, err
		}
		if result.Score >= request.Options.MinScore && len(results) < request.Options.ResultLimit() {
			results = append(results, result)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("clickhouse: read search rows: %w", err)
	}
	return &vectorstore.SearchResponse{Results: results}, nil
}

// DeleteWhere validates the complete current-record snapshot before any delete.
// Every page is applied and published by the same native transaction.
func (s *Store) DeleteWhere(ctx context.Context, predicate filter.Predicate) (err error) {
	if predicate == nil {
		return vectorstore.ErrMissingFilter
	}
	if err = predicate.Validate(); err != nil {
		return fmt.Errorf("clickhouse.Store.DeleteWhere: %w", err)
	}
	transaction, err := newNativeTransaction(ctx, s.db)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, transaction.Close()) }()
	matches, err := s.preflightFilter(transaction, predicate)
	if err != nil {
		return err
	}
	if matches {
		var after *string
		for {
			docs, err := s.metadataPage(transaction, after)
			if err != nil {
				return err
			}
			if len(docs) == 0 {
				break
			}
			lastID := docs[len(docs)-1].ID
			after = &lastID
			ids, err := s.matchingIDs(docs, predicate)
			if err != nil {
				return err
			}
			if err := s.deleteIDs(transaction, ids); err != nil {
				return err
			}
		}
	}
	return transaction.Commit()
}

// DeleteIDs removes the IDs visible in one native snapshot. Empty input is a no-op.
func (s *Store) DeleteIDs(ctx context.Context, ids []string) (err error) {
	if len(ids) == 0 {
		return nil
	}
	transaction, err := newNativeTransaction(ctx, s.db)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, transaction.Close()) }()
	for start := 0; start < len(ids); start += metadataPageSize {
		if err := s.deleteIDs(transaction, ids[start:min(start+metadataPageSize, len(ids))]); err != nil {
			return err
		}
	}
	return transaction.Commit()
}

func (s *Store) deleteIDs(transaction *nativeTransaction, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	statement := fmt.Sprintf("DELETE FROM %s WHERE %s IN {scope_ids:Array(String)}", s.fullTable, s.idColumn)
	if _, err := transaction.Exec(statement, sql.Named("scope_ids", ids)); err != nil {
		return fmt.Errorf("clickhouse: delete IDs from %s: %w", s.fullTable, err)
	}
	return nil
}

const metadataPageSize = 512

func (s *Store) metadataPage(transaction *nativeTransaction, after *string) (docs []*document.Document, err error) {
	statement := fmt.Sprintf("SELECT %s, %s, %s FROM %s FINAL", s.idColumn, s.contentColumn, s.metadataColumn, s.fullTable)
	var args []any
	if after != nil {
		statement += fmt.Sprintf(" WHERE %s > {scope_cursor:String}", s.idColumn)
		args = append(args, sql.Named("scope_cursor", *after))
	}
	statement += fmt.Sprintf(" ORDER BY %s LIMIT %d", s.idColumn, metadataPageSize)
	rows, err := transaction.Query(statement, args...)
	if err != nil {
		return nil, fmt.Errorf("clickhouse: read metadata: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("clickhouse: close metadata rows: %w", closeErr))
		}
		if err != nil {
			docs = nil
		}
	}()
	for rows.Next() {
		doc := &document.Document{}
		var encoded string
		if err := rows.Scan(&doc.ID, &doc.Text, &encoded); err != nil {
			return nil, err
		}
		if err := doc.Metadata.UnmarshalJSON([]byte(encoded)); err != nil {
			return nil, fmt.Errorf("clickhouse: decode metadata for %q: %w", doc.ID, err)
		}
		docs = append(docs, doc)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(docs) > 0 {
		if err := (&vectorstore.IndexRequest{Documents: docs}).Validate(); err != nil {
			return nil, fmt.Errorf("clickhouse: invalid stored documents: %w", err)
		}
	}
	return docs, nil
}

func (s *Store) matchingIDs(docs []*document.Document, predicate filter.Predicate) ([]string, error) {
	var ids []string
	for _, doc := range docs {
		values, err := doc.Metadata.Values()
		if err != nil {
			return nil, err
		}
		matches, err := filter.Match(predicate, values)
		if err != nil {
			return nil, fmt.Errorf("clickhouse: evaluate metadata for %q: %w", doc.ID, err)
		}
		if matches {
			ids = append(ids, doc.ID)
		}
	}
	return ids, nil
}

func (s *Store) preflightFilter(transaction *nativeTransaction, predicate filter.Predicate) (bool, error) {
	var after *string
	matches := false
	for {
		docs, err := s.metadataPage(transaction, after)
		if err != nil {
			return false, err
		}
		if len(docs) == 0 {
			return matches, nil
		}
		ids, err := s.matchingIDs(docs, predicate)
		if err != nil {
			return false, err
		}
		matches = matches || len(ids) > 0
		lastID := docs[len(docs)-1].ID
		after = &lastID
	}
}

func (s *Store) validateTable(ctx context.Context) (err error) {
	ctx = nativeStatementContext(ctx, false)
	var engine, sortingKey, partitionKey, createStatement string
	if err = s.db.QueryRowContext(ctx, "SELECT engine_full, sorting_key, partition_key, formatQuery(create_table_query) FROM system.tables WHERE database = ? AND name = ?", s.databaseName, s.tableName).Scan(&engine, &sortingKey, &partitionKey, &createStatement); err != nil {
		return fmt.Errorf("clickhouse: inspect table %s: %w", s.fullTable, err)
	}
	engineName, _, _ := strings.Cut(engine, " ORDER BY ")
	for strings.HasPrefix(sortingKey, "(") && strings.HasSuffix(sortingKey, ")") {
		sortingKey = sortingKey[1 : len(sortingKey)-1]
	}
	if (engineName != "ReplacingMergeTree" && engineName != "ReplacingMergeTree()") || sortingKey != s.idColumn || partitionKey != "" {
		return fmt.Errorf("clickhouse: table %s must use unpartitioned ReplacingMergeTree without version arguments, ordered by %s", s.fullTable, s.idColumn)
	}
	widthPattern := `(?m)^[\t ]*CONSTRAINT vec_len CHECK length\(` + regexp.QuoteMeta(s.embeddingColumn) + `\) = [1-9][0-9]*[,\n]`
	validWidth, err := regexp.MatchString(widthPattern, createStatement)
	if err != nil {
		return err
	}
	finitePattern := `(?m)^[\t ]*CONSTRAINT vec_finite CHECK arrayAll\(x -> isFinite\(x\), ` + regexp.QuoteMeta(s.embeddingColumn) + `\)[,\n]`
	validFinite, err := regexp.MatchString(finitePattern, createStatement)
	if err != nil {
		return err
	}
	if !validWidth || !validFinite {
		return fmt.Errorf("clickhouse: table %s must have current vec_len and vec_finite constraints", s.fullTable)
	}
	expected := map[string]string{s.idColumn: "String", s.contentColumn: "String", s.metadataColumn: "String", s.embeddingColumn: "Array(Float32)"}
	rows, err := s.db.QueryContext(ctx, "SELECT name, type, default_kind FROM system.columns WHERE database = ? AND table = ? AND name IN (?, ?, ?, ?)", s.databaseName, s.tableName, s.idColumn, s.contentColumn, s.metadataColumn, s.embeddingColumn)
	if err != nil {
		return fmt.Errorf("clickhouse: inspect columns: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("clickhouse: close schema rows: %w", closeErr))
		}
	}()
	for rows.Next() {
		var name, dataType, defaultKind string
		if err := rows.Scan(&name, &dataType, &defaultKind); err != nil {
			return err
		}
		if expected[name] != dataType || defaultKind != "" {
			return fmt.Errorf("clickhouse: column %q must use %s without generated values, got %s %s", name, expected[name], dataType, defaultKind)
		}
		delete(expected, name)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(expected) != 0 {
		return fmt.Errorf("clickhouse: table %s is missing required columns", s.fullTable)
	}
	return nil
}
