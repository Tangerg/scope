package clickhouse

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Tangerg/scope/core/vectorstore/filter"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
)

const Provider = "ClickHouse"

const (
	DefaultTableName       = "vector_store"
	DefaultIDColumn        = "id"
	DefaultContentColumn   = "content"
	DefaultMetadataColumn  = "metadata"
	DefaultEmbeddingColumn = "embedding"
	DefaultDistanceMetric  = DistanceCosine
)

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

	DatabaseName string

	TableName       string
	IDColumn        string
	ContentColumn   string
	MetadataColumn  string
	EmbeddingColumn string

	Space vectorstore.SpaceID

	Dimensions       int
	DistanceMetric   DistanceMetric
	InitializeSchema bool
}

func (s StoreConfig) Validate() error {
	s.applyDefaults()
	if s.DB == nil {
		return errors.New("clickhouse: DB is required")
	}
	if err := s.Space.Validate(); err != nil {
		return err
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

func (s *StoreConfig) applyDefaults() {
	s.TableName = cmp.Or(s.TableName, DefaultTableName)
	s.IDColumn = cmp.Or(s.IDColumn, DefaultIDColumn)
	s.ContentColumn = cmp.Or(s.ContentColumn, DefaultContentColumn)
	s.MetadataColumn = cmp.Or(s.MetadataColumn, DefaultMetadataColumn)
	s.EmbeddingColumn = cmp.Or(s.EmbeddingColumn, DefaultEmbeddingColumn)
	s.DistanceMetric = cmp.Or(s.DistanceMetric, DefaultDistanceMetric)
}

var (
	_ vectorstore.Upserter  = (*Store)(nil)
	_ vectorstore.Querier   = (*Store)(nil)
	_ vectorstore.IDDeleter = (*Store)(nil)
)

type Store struct {
	db              *sql.DB
	databaseName    string
	tableName       string
	fullTable       string
	idColumn        string
	contentColumn   string
	metadataColumn  string
	embeddingColumn string
	space           vectorstore.SpaceID
	dimensions      int
	distanceMetric  DistanceMetric
}

// NewStore verifies native transaction support and the strict current schema.
// An omitted database is resolved once; every operation uses its qualified table.
func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	config.applyDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}
	transaction, err := newNativeTransaction(ctx, config.DB)
	if err != nil {
		return nil, fmt.Errorf("clickhouse: native transactions are required: %w", err)
	}
	if err = transaction.Close(); err != nil {
		return nil, err
	}
	if config.DatabaseName == "" {
		if err = config.DB.QueryRowContext(nativeStatementContext(ctx, false), "SELECT currentDatabase()").Scan(&config.DatabaseName); err != nil {
			return nil, fmt.Errorf("clickhouse: resolve database: %w", err)
		}
		if err = identifier(config.DatabaseName).validate("DatabaseName"); err != nil {
			return nil, err
		}
	}
	physical := config.Space.StorageKey() + "_" + config.TableName
	store := &Store{
		db: config.DB, databaseName: config.DatabaseName, tableName: physical, fullTable: config.DatabaseName + "." + physical,
		idColumn: config.IDColumn, contentColumn: config.ContentColumn, metadataColumn: config.MetadataColumn, embeddingColumn: config.EmbeddingColumn,
		space: config.Space, distanceMetric: config.DistanceMetric,
	}
	if config.InitializeSchema {
		if err = store.initialize(ctx, config.Dimensions); err != nil {
			return nil, err
		}
	}
	width, err := store.tableDimensions(ctx)
	if err != nil {
		return nil, err
	}
	store.dimensions = width
	if config.Dimensions > 0 && config.Dimensions != width {
		return nil, fmt.Errorf("%w: requested %d dimensions, table declares %d", vectorstore.ErrIncompatibleSpace, config.Dimensions, width)
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
	for _, d := range docs {
		if d.Media != nil {
			return vectorstore.ErrInvalidDocument
		}
	}
	return nil
}
func (s *Store) narrow(values []float64) ([]float32, error) {
	if len(values) != s.dimensions {
		return nil, fmt.Errorf("%w: %d coordinates for ClickHouse width %d", vectorstore.ErrIncompatibleSpace, len(values), s.dimensions)
	}
	v := make([]float32, len(values))
	nonzero := false
	for i, x := range values {
		v[i] = float32(x)
		if math.IsInf(float64(v[i]), 0) || math.IsNaN(float64(v[i])) {
			return nil, fmt.Errorf("%w: nonfinite float32", vectorstore.ErrInvalidRequest)
		}
		nonzero = nonzero || v[i] != 0
	}
	if !nonzero && s.distanceMetric == DistanceCosine {
		return nil, fmt.Errorf("%w: cosine requires a nonzero vector", vectorstore.ErrInvalidRequest)
	}
	return v, nil
}

type insertRecord struct {
	id, text, facts string
	vector          []float32
}

// Upsert prepares every row before opening a driver buffer. Only Commit sends
// that single native INSERT. A lost response cannot identify individual rows.
func (s *Store) Upsert(ctx context.Context, request *vectorstore.Batch) (err error) {
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = request.ValidateFor(s.space); err != nil {
		return err
	}
	rows := make([]insertRecord, len(request.Records))
	ids := make([]string, len(rows))
	for i, r := range request.Records {
		if r.Document.Media != nil {
			return vectorstore.ErrInvalidDocument
		}
		v, narrowErr := s.narrow(r.Values)
		if narrowErr != nil {
			return narrowErr
		}
		facts, encodeErr := document.MarshalStoredMetadata(r.Document)
		if encodeErr != nil {
			return encodeErr
		}
		rows[i] = insertRecord{id: r.Document.ID, text: r.Document.Text, facts: string(facts), vector: v}
		ids[i] = r.Document.ID
	}
	attempted, acknowledged := false, false
	defer func() {
		if err != nil {
			failure := &vectorstore.WriteError{Err: err}
			switch {
			case acknowledged:
				failure.Acknowledged = ids
			case attempted:
				failure.Unconfirmed = ids
			default:
				failure.Unattempted = ids
			}
			err = failure
		}
	}()
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
	for _, r := range rows {
		if _, err := statement.ExecContext(ctx, r.id, r.text, r.facts, r.vector); err != nil {
			return fmt.Errorf("clickhouse: append %q: %w", r.id, err)
		}
	}
	attempted = true
	if err := batch.Commit(); err != nil {
		return fmt.Errorf("clickhouse: send insert batch: %w", err)
	}
	acknowledged = true
	return nil
}

func (s *Store) CheckQuery(ctx context.Context, space vectorstore.SpaceID, opts vectorstore.SearchOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := space.Require(s.space); err != nil {
		return err
	}
	if err := opts.Validate(); err != nil {
		return err
	}
	if err := opts.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return err
	}
	if opts.Filter != nil {
		return vectorstore.ErrUnsupportedFilter
	}
	return nil
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
	defer func() {
		if err == nil {
			err = request.ValidateResponse(response)
		}
		if err != nil {
			response = nil
		}
	}()
	vector, err := s.narrow(request.Values)
	if err != nil {
		return nil, err
	}
	statement := fmt.Sprintf("SELECT %s, %s, %s, toFloat64(%s(%s, {scope_vector:Array(Float32)})) AS distance FROM %s FINAL ORDER BY distance, %s", s.idColumn, s.contentColumn, s.metadataColumn, s.distanceMetric.function(), s.embeddingColumn, s.fullTable, s.idColumn)
	statement += " LIMIT {scope_limit:UInt64}"
	rows, err := s.db.QueryContext(nativeStatementContext(ctx, true), statement, sql.Named("scope_vector", vector), sql.Named("scope_limit", request.Options.ResultLimit()))
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
	seen := make(map[string]bool)
	for rows.Next() {
		doc := &document.Document{}
		var encoded string
		var distance float64
		if err := rows.Scan(&doc.ID, &doc.Text, &encoded, &distance); err != nil {
			return nil, fmt.Errorf("clickhouse: scan search row: %w", err)
		}
		if err := document.UnmarshalStoredMetadata([]byte(encoded), doc); err != nil {
			return nil, fmt.Errorf("clickhouse: decode metadata for %q: %w", doc.ID, err)
		}
		if math.IsNaN(distance) || math.IsInf(distance, 0) || distance < 0 || s.distanceMetric == DistanceCosine && distance > 2 {
			return nil, fmt.Errorf("clickhouse: %w: invalid native distance", vectorstore.ErrInvalidScore)
		}
		if seen[doc.ID] || len(seen) >= request.Options.ResultLimit() {
			return nil, fmt.Errorf("clickhouse: %w: duplicate or excess rows", vectorstore.ErrInvalidResponse)
		}
		seen[doc.ID] = true
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
	return s.deleteSelected(transaction, ids)
}

func (s *Store) deleteSelected(transaction *nativeTransaction, ids []string) error {
	for chunk := range slices.Chunk(ids, metadataPageSize) {
		if err := s.deleteIDs(transaction, chunk); err != nil {
			return err
		}
	}
	if err := transaction.Commit(); err != nil {
		return &vectorstore.WriteError{Err: err, Unconfirmed: slices.Clone(ids)}
	}
	return nil
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

func (s *Store) tableDimensions(ctx context.Context) (width int, err error) {
	ctx = nativeStatementContext(ctx, false)
	var engine, sortingKey, partitionKey, createStatement string
	if err = s.db.QueryRowContext(ctx, "SELECT engine_full, sorting_key, partition_key, formatQuery(create_table_query) FROM system.tables WHERE database = ? AND name = ?", s.databaseName, s.tableName).Scan(&engine, &sortingKey, &partitionKey, &createStatement); err != nil {
		return 0, fmt.Errorf("clickhouse: inspect table %s: %w", s.fullTable, err)
	}
	engineName, _, _ := strings.Cut(engine, " ORDER BY ")
	for strings.HasPrefix(sortingKey, "(") && strings.HasSuffix(sortingKey, ")") {
		sortingKey = sortingKey[1 : len(sortingKey)-1]
	}
	if (engineName != "ReplacingMergeTree" && engineName != "ReplacingMergeTree()") || sortingKey != s.idColumn || partitionKey != "" {
		return 0, fmt.Errorf("clickhouse: table %s must use unpartitioned ReplacingMergeTree without version arguments, ordered by %s", s.fullTable, s.idColumn)
	}
	widthPattern := `(?m)^[\t ]*CONSTRAINT vec_len CHECK length\(` + regexp.QuoteMeta(s.embeddingColumn) + `\) = ([1-9][0-9]*)[,\n]`
	widthRE, err := regexp.Compile(widthPattern)
	if err != nil {
		return 0, err
	}
	matches := widthRE.FindStringSubmatch(createStatement)
	if len(matches) != 2 {
		return 0, fmt.Errorf("%w: missing native width constraint", vectorstore.ErrIncompatibleSpace)
	}
	width, err = strconv.Atoi(matches[1])
	if err != nil {
		return 0, err
	}
	finitePattern := `(?m)^[\t ]*CONSTRAINT vec_finite CHECK arrayAll\(x -> isFinite\(x\), ` + regexp.QuoteMeta(s.embeddingColumn) + `\)[,\n]`
	validFinite, err := regexp.MatchString(finitePattern, createStatement)
	if err != nil {
		return 0, err
	}
	if !validFinite {
		return 0, fmt.Errorf("clickhouse: table %s must have current vec_len and vec_finite constraints", s.fullTable)
	}
	expected := map[string]string{s.idColumn: "String", s.contentColumn: "String", s.metadataColumn: "String", s.embeddingColumn: "Array(Float32)"}
	rows, err := s.db.QueryContext(ctx, "SELECT name, type, default_kind FROM system.columns WHERE database = ? AND table = ?", s.databaseName, s.tableName)
	if err != nil {
		return 0, fmt.Errorf("clickhouse: inspect columns: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("clickhouse: close schema rows: %w", closeErr))
		}
	}()
	for rows.Next() {
		var name, dataType, defaultKind string
		if err := rows.Scan(&name, &dataType, &defaultKind); err != nil {
			return 0, err
		}
		if expected[name] != dataType || defaultKind != "" {
			return 0, fmt.Errorf("clickhouse: column %q must use %s without generated values, got %s %s", name, expected[name], dataType, defaultKind)
		}
		delete(expected, name)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(expected) != 0 {
		return 0, fmt.Errorf("clickhouse: table %s is missing required columns", s.fullTable)
	}
	return width, nil
}

// scan visits FINAL records in one native transaction. It charges every
// selected ID, content and raw metadata byte before decoding the predicate.
func (s *Store) scan(tx *nativeTransaction, meter *vectorstore.ScanMeter, p filter.Predicate) ([]*document.Document, error) {
	var after string
	var docs []*document.Document
	for {
		query := fmt.Sprintf("SELECT %s, %s, %s FROM %s FINAL", s.idColumn, s.contentColumn, s.metadataColumn, s.fullTable)
		var args []any
		if after != "" {
			query += fmt.Sprintf(" WHERE %s > {scope_cursor:String}", s.idColumn)
			args = append(args, sql.Named("scope_cursor", after))
		}
		query += fmt.Sprintf(" ORDER BY %s LIMIT %d", s.idColumn, metadataPageSize)
		rows, err := tx.Query(query, args...)
		if err != nil {
			return nil, err
		}
		count := 0
		err = func() (err error) {
			defer func() { err = errors.Join(err, rows.Close()) }()
			for rows.Next() {
				count++
				if count > metadataPageSize {
					return errors.New("clickhouse: source exceeded scan page")
				}
				d := new(document.Document)
				var raw string
				if err := rows.Scan(&d.ID, &d.Text, &raw); err != nil {
					return err
				}
				if err := meter.Charge(int64(len(d.ID)) + int64(len(d.Text)) + int64(len(raw))); err != nil {
					return err
				}
				if d.ID <= after {
					return errors.New("clickhouse: scan key failed to advance")
				}
				after = d.ID
				if err := document.UnmarshalStoredMetadata([]byte(raw), d); err != nil {
					return err
				}
				if err := vectorstore.ValidateDocument(d); err != nil {
					return err
				}
				if p != nil {
					m, err := d.Metadata.Values()
					if err != nil {
						return err
					}
					ok, err := filter.Match(p, m)
					if err != nil {
						return err
					}
					if !ok {
						continue
					}
				}
				docs = append(docs, d)
			}
			return rows.Err()
		}()
		if err != nil {
			return nil, err
		}
		if count == 0 {
			return docs, meter.Context().Err()
		}
	}
}

// Scan returns no partial records. Its grant covers a single native snapshot.
func (s *Store) Scan(ctx context.Context, cfg ScanConfig) (found []*document.Document, err error) {
	if err = cfg.Validate(); err != nil {
		return nil, err
	}
	m, err := cfg.Budget.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer m.Close()
	tx, err := newNativeTransaction(m.Context(), s.db)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, tx.Close())
		if err != nil {
			found = nil
		}
	}()
	found, err = s.scan(tx, m, cfg.Filter)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return found, nil
}

// DeleteMatching proves membership over the complete bounded snapshot before
// buffering deletes on the same session. Only native COMMIT publishes them.
func (s *Store) DeleteMatching(ctx context.Context, cfg ScanConfig) (err error) {
	if cfg.Filter == nil {
		return vectorstore.ErrMissingFilter
	}
	if err = cfg.Validate(); err != nil {
		return err
	}
	m, err := cfg.Budget.Begin(ctx)
	if err != nil {
		return err
	}
	defer m.Close()
	tx, err := newNativeTransaction(m.Context(), s.db)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, tx.Close()) }()
	docs, err := s.scan(tx, m, cfg.Filter)
	if err != nil {
		return err
	}
	ids := make([]string, len(docs))
	for i, d := range docs {
		ids[i] = d.ID
	}
	return s.deleteSelected(tx, ids)
}
