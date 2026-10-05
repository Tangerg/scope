package mariadb

import (
	"cmp"
	"context"
	"database/sql"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// Provider is the stable backend name for host-side attribution.
const Provider = "MariaDB"

const documentIDBytes = 3072

// Exported defaults keep constructor behavior visible and overridable.
const (
	DefaultTableName       = "vector_store"
	DefaultIDColumn        = "id"
	DefaultContentColumn   = "content"
	DefaultMetadataColumn  = "metadata"
	DefaultEmbeddingColumn = "embedding"
	DefaultDistanceMetric  = DistanceCosine
)

// DistanceMetric selects the vec_distance_<metric> function used for ranking.
type DistanceMetric string

const (
	// DistanceCosine — cosine distance. Default.
	DistanceCosine DistanceMetric = "cosine"

	// DistanceEuclidean — Euclidean (L2) distance.
	DistanceEuclidean DistanceMetric = "euclidean"
)

func (d DistanceMetric) Valid() bool {
	return d == DistanceCosine || d == DistanceEuclidean
}

func (d DistanceMetric) String() string { return string(d) }

func (d DistanceMetric) score(distance float64) vectorstore.Score {
	switch d {
	case DistanceEuclidean:
		return vectorstore.ScoreFromDistance(distance)
	case DistanceCosine:
		fallthrough
	default:
		return vectorstore.ScoreFromCosineDistance(distance)
	}
}

// StoreConfig contains configuration options for the MariaDB vector
// store.
type StoreConfig struct {
	// DB is the database handle. Required. Use a *sql.DB built from
	// the github.com/go-sql-driver/mysql driver pointed at a MariaDB
	// 11.7+ instance (or Enterprise Server 11.4.5-3+) with vector support.
	DB *sql.DB

	// SchemaName is the optional schema (database) prefix. When
	// empty the connection's default database is used.
	SchemaName string

	// TableName is the table that stores documents and their
	// embeddings. Optional: defaults to [DefaultTableName].
	TableName string

	// IDColumn / ContentColumn / MetadataColumn / EmbeddingColumn
	// override the column names of the generated schema. Each
	// defaults to its respective Default* constant when empty.
	IDColumn        string
	ContentColumn   string
	MetadataColumn  string
	EmbeddingColumn string

	// EmbeddingModel produces vectors for the documents. Required.
	EmbeddingModel embedding.Model

	// DocumentBatcher batches documents before insertion. Required.
	DocumentBatcher vectorstore.Batcher

	// Dimensions sets the VECTOR column width, and is required when
	// InitializeSchema is true: the width is part of the column type, and
	// nothing here can read it off a table that does not exist yet.
	Dimensions int

	// DistanceMetric selects the distance function. Optional:
	// defaults to [DistanceCosine].
	DistanceMetric DistanceMetric

	// InitializeSchema creates the table when absent. NewStore
	// always verifies the current identity schema; obsolete tables must be rebuilt.
	InitializeSchema bool
}

func (s StoreConfig) Validate() error {
	s.applyDefaults()
	if s.DB == nil {
		return errors.New("mariadb: DB is required")
	}
	if lo.IsNil(s.EmbeddingModel) {
		return errors.New("mariadb: EmbeddingModel is required")
	}
	if lo.IsNil(s.DocumentBatcher) {
		return errors.New("mariadb: DocumentBatcher is required")
	}
	if s.Dimensions < 0 {
		return errors.New("mariadb: Dimensions must be >= 0")
	}
	if !s.DistanceMetric.Valid() {
		return fmt.Errorf("mariadb: unsupported DistanceMetric %q", s.DistanceMetric)
	}
	return s.validateIdentifiers()
}

func (s StoreConfig) validateIdentifiers() error {
	if s.SchemaName != "" {
		if err := identifier(s.SchemaName).validate("SchemaName"); err != nil {
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

// Store implements vector-store capabilities with the VECTOR column type and
// vec_distance_* functions introduced in MariaDB Community Server 11.7.
type Store struct {
	db              *sql.DB
	schemaName      string
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

// NewStore creates the schema when requested and verifies that the table's
// constraints preserve exact document IDs. The caller owns the database handle.
func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	config.applyDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}

	embeddingClient, err := embeddingclient.New(config.EmbeddingModel)
	if err != nil {
		return nil, fmt.Errorf("mariadb: create embedding client: %w", err)
	}

	fullTable := config.TableName
	if config.SchemaName != "" {
		fullTable = config.SchemaName + "." + config.TableName
	}

	store := &Store{
		db:              config.DB,
		schemaName:      config.SchemaName,
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
		return nil, fmt.Errorf("mariadb: initialize store: %w", err)
	}
	return store, nil
}

// initialize provisions the table when requested.
func (s *Store) initialize(ctx context.Context, initSchema bool) error {
	if !initSchema {
		return s.validateIdentitySchema(ctx)
	}
	if s.dimensions <= 0 {
		return errors.New("mariadb: Dimensions must be > 0")
	}

	if s.schemaName != "" {
		if _, err := s.db.ExecContext(ctx,
			fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", s.schemaName)); err != nil {
			return fmt.Errorf("create schema %s: %w", s.schemaName, err)
		}
	}

	if _, err := s.db.ExecContext(ctx, s.createTableStatement()); err != nil {
		return fmt.Errorf("create table %s: %w", s.fullTable, err)
	}
	return s.validateIdentitySchema(ctx)
}

func (s *Store) validateIdentitySchema(ctx context.Context) (err error) {
	const database = "COALESCE(NULLIF(?, ''), DATABASE())"
	var dataType, nullable, engine string
	var length sql.NullInt64
	err = s.db.QueryRowContext(ctx,
		"SELECT c.DATA_TYPE, c.CHARACTER_MAXIMUM_LENGTH, c.IS_NULLABLE, t.ENGINE FROM information_schema.COLUMNS AS c JOIN information_schema.TABLES AS t ON t.TABLE_SCHEMA = c.TABLE_SCHEMA AND t.TABLE_NAME = c.TABLE_NAME WHERE c.TABLE_SCHEMA = "+database+" AND c.TABLE_NAME = ? AND c.COLUMN_NAME = ?",
		s.schemaName, s.tableName, s.idColumn).Scan(&dataType, &length, &nullable, &engine)
	if err != nil {
		return fmt.Errorf("read document ID schema: %w", err)
	}
	if dataType != "varbinary" || !length.Valid || length.Int64 != documentIDBytes || nullable != "NO" {
		return fmt.Errorf("document ID column must be VARBINARY(%d) NOT NULL; rebuild the table", documentIDBytes)
	}
	if engine != "InnoDB" {
		return errors.New("filter transactions require an InnoDB table; rebuild the table")
	}
	rows, err := s.db.QueryContext(ctx,
		"SELECT INDEX_NAME, COLUMN_NAME, SEQ_IN_INDEX, SUB_PART FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = "+database+" AND TABLE_NAME = ? AND NON_UNIQUE = 0",
		s.schemaName, s.tableName)
	if err != nil {
		return fmt.Errorf("read document identity constraints: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	primary := false
	for rows.Next() {
		var name, column string
		var ordinal int
		var prefix sql.NullInt64
		if err := rows.Scan(&name, &column, &ordinal, &prefix); err != nil {
			return fmt.Errorf("read document identity constraint: %w", err)
		}
		if !strings.EqualFold(column, s.idColumn) || ordinal != 1 || prefix.Valid {
			return errors.New("unique constraints must use the entire document ID as their sole key; rebuild the table")
		}
		primary = primary || name == "PRIMARY"
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read document identity constraints: %w", err)
	}
	if !primary {
		return errors.New("document ID must be the sole primary key; rebuild the table")
	}
	return nil
}

// The native vector index can omit existing rows after replacement. Reading
// the primary table keeps that projection out of document visibility, including
// tables provisioned externally with a vector index.
func (s *Store) searchStatement(wherePart string) string {
	return fmt.Sprintf(
		`SELECT %s, %s, %s, vec_distance_%s(%s, VEC_FromText(?)) AS distance `+
			`FROM %s FORCE INDEX(PRIMARY) WHERE 1=1%s ORDER BY distance ASC, %s ASC LIMIT ?`,
		s.idColumn, s.contentColumn, s.metadataColumn,
		s.distanceMetric, s.embeddingColumn,
		s.fullTable, wherePart, s.idColumn,
	)
}

func (s *Store) createTableStatement() string {
	return fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %s (
			%s VARBINARY(%d) NOT NULL PRIMARY KEY,
			%s TEXT,
			%s JSON,
			%s VECTOR(%d) NOT NULL
		) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4`,
		s.fullTable,
		s.idColumn, documentIDBytes,
		s.contentColumn,
		s.metadataColumn,
		s.embeddingColumn, s.dimensions,
	)
}

// Index embeds documents and upserts them into the vector table.
func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) (err error) {
	if validateErr := request.Validate(); validateErr != nil {
		return fmt.Errorf("mariadb.Store.Index: %w", validateErr)
	}
	for index, doc := range request.Documents {
		if len(doc.ID) > documentIDBytes {
			return fmt.Errorf("mariadb.Store.Index: %w: documents[%d] ID exceeds %d bytes", vectorstore.ErrInvalidDocument, index, documentIDBytes)
		}
		if doc.Media != nil {
			return fmt.Errorf("mariadb.Store.Index: %w: documents[%d] contains unsupported media", vectorstore.ErrInvalidDocument, index)
		}
	}

	var batches []*vectorstore.IndexRequest
	batches, err = request.Batch(ctx, s.documentBatcher)
	if err != nil {
		return fmt.Errorf("mariadb: batch documents: %w", err)
	}

	upsert := fmt.Sprintf(
		`INSERT INTO %s (%s, %s, %s, %s) VALUES (?, ?, ?, VEC_FromText(?)) `+
			`ON DUPLICATE KEY UPDATE %s = VALUES(%s), %s = VALUES(%s), %s = VALUES(%s)`,
		s.fullTable, s.idColumn, s.contentColumn, s.metadataColumn, s.embeddingColumn,
		s.contentColumn, s.contentColumn,
		s.metadataColumn, s.metadataColumn,
		s.embeddingColumn, s.embeddingColumn,
	)

	for _, batch := range batches {
		docs := batch.Documents
		texts, err := batch.Texts()
		if err != nil {
			return fmt.Errorf("vectorstore: project document text: %w", err)
		}
		vectors, err := s.embeddingClient.EmbedTexts(ctx, texts)
		if err != nil {
			return fmt.Errorf("mariadb: embed documents: %w", err)
		}

		stmt, err := s.db.PrepareContext(ctx, upsert)
		if err != nil {
			return fmt.Errorf("mariadb: prepare upsert: %w", err)
		}

		execErr := func() (err error) {
			defer func() {
				if closeErr := stmt.Close(); closeErr != nil {
					err = errors.Join(err, closeErr)
				}
			}()
			for i, doc := range docs {
				id := doc.ID
				metaJSON, err := jsonv2.Marshal(doc.Metadata)
				if err != nil {
					return fmt.Errorf("marshal metadata for %s: %w", id, err)
				}
				vectorJSON, err := jsonv2.Marshal(embedding.Float32Vector(vectors[i]))
				if err != nil {
					return fmt.Errorf("mariadb: marshal vector for %s: %w", id, err)
				}
				if _, err := stmt.ExecContext(ctx, []byte(id), doc.Text, metaJSON, string(vectorJSON)); err != nil {
					return fmt.Errorf("upsert %s: %w", id, err)
				}
			}
			return nil
		}()
		if execErr != nil {
			return execErr
		}
	}
	return nil
}

// Search ranks canonical rows using the native distance function. Core alone
// evaluates filters; matching IDs are bounded projections of the same snapshot.
func (s *Store) Search(ctx context.Context, request *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	if err = request.Validate(); err != nil {
		return nil, fmt.Errorf("mariadb.Store.Search: %w", err)
	}
	if err = request.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, fmt.Errorf("mariadb.Store.Search: %w", err)
	}
	defer func() {
		if err == nil {
			err = response.ValidateFor(request)
		}
		if err != nil {
			response = nil
		}
	}()
	transaction, err := s.prepareFilter(ctx, request.Options.Filter, true)
	defer finishTransaction(transaction, &err)
	if err != nil {
		return nil, err
	}
	vector, err := s.embeddingClient.EmbedText(ctx, request.Query)
	if err != nil {
		return nil, fmt.Errorf("mariadb: embed query: %w", err)
	}
	encoded, err := jsonv2.Marshal(embedding.Float32Vector(vector))
	if err != nil {
		return nil, fmt.Errorf("mariadb: marshal query vector: %w", err)
	}
	var ranked []rankedResult
	if request.Options.Filter == nil {
		ranked, err = s.searchRows(ctx, nil, request, string(encoded), nil)
		if err != nil {
			return nil, err
		}
	} else {
		var lastID []byte
		for {
			page, pageErr := s.readFilterPage(ctx, transaction, request.Options.Filter, lastID)
			if pageErr != nil {
				return nil, pageErr
			}
			if len(page.ids) > 0 {
				candidates, queryErr := s.searchRows(ctx, transaction, request, string(encoded), page.ids)
				if queryErr != nil {
					return nil, queryErr
				}
				ranked = append(ranked, candidates...)
				slices.SortFunc(ranked, func(left, right rankedResult) int {
					if order := cmp.Compare(left.distance, right.distance); order != 0 {
						return order
					}
					return strings.Compare(left.result.Document.ID, right.result.Document.ID)
				})
				ranked = ranked[:min(len(ranked), request.Options.ResultLimit())]
			}
			if page.count < filterPageSize {
				break
			}
			lastID = page.lastID
		}
	}
	results := make([]*vectorstore.SearchResult, len(ranked))
	for index, candidate := range ranked {
		results[index] = candidate.result
	}
	return &vectorstore.SearchResponse{Results: results}, nil
}

type rankedResult struct {
	result   *vectorstore.SearchResult
	distance float64
}

func (s *Store) searchRows(ctx context.Context, transaction *sql.Tx, request *vectorstore.SearchRequest, vector string, ids []any) (results []rankedResult, err error) {
	where := ""
	if len(ids) > 0 {
		where = " AND " + s.idColumn + " IN (" + strings.Repeat("?, ", len(ids)-1) + "?)"
	}
	args := []any{vector}
	args = append(args, ids...)
	args = append(args, request.Options.ResultLimit())
	var rows *sql.Rows
	if transaction == nil {
		rows, err = s.db.QueryContext(ctx, s.searchStatement(where), args...)
	} else {
		rows, err = transaction.QueryContext(ctx, s.searchStatement(where), args...)
	}
	if err != nil {
		return nil, fmt.Errorf("mariadb: query %s: %w", s.fullTable, err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	for rows.Next() {
		var id string
		var content, raw sql.NullString
		var distance float64
		if err := rows.Scan(&id, &content, &raw, &distance); err != nil {
			return nil, fmt.Errorf("mariadb: scan row: %w", err)
		}
		score := s.distanceMetric.score(distance)
		if err := score.Validate(); err != nil {
			return nil, fmt.Errorf("mariadb: distance for %q: %w", id, err)
		}
		doc := &document.Document{ID: id, Text: content.String}
		if raw.Valid {
			if err := jsonv2.Unmarshal([]byte(raw.String), &doc.Metadata); err != nil {
				return nil, fmt.Errorf("mariadb: unmarshal metadata for %s: %w", id, err)
			}
		}
		if err := (&vectorstore.IndexRequest{Documents: []*document.Document{doc}}).Validate(); err != nil {
			return nil, fmt.Errorf("mariadb: invalid native document %q: %w", id, err)
		}
		if score < request.Options.MinScore {
			continue
		}
		results = append(results, rankedResult{result: &vectorstore.SearchResult{Document: doc, Score: score}, distance: distance})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mariadb: read rows: %w", err)
	}
	return results, nil
}

func (s *Store) DeleteWhere(ctx context.Context, predicate filter.Predicate) (err error) {
	if predicate == nil {
		return vectorstore.ErrMissingFilter
	}
	if validateErr := predicate.Validate(); validateErr != nil {
		return fmt.Errorf("mariadb.Store.DeleteWhere: %w", validateErr)
	}
	transaction, err := s.prepareFilter(ctx, predicate, false)
	defer finishTransaction(transaction, &err)
	if err != nil {
		return err
	}
	var lastID []byte
	for {
		page, pageErr := s.readFilterPage(ctx, transaction, predicate, lastID)
		if pageErr != nil {
			return pageErr
		}
		if err := s.deleteIDs(ctx, transaction, page.ids); err != nil {
			return err
		}
		if page.count < filterPageSize {
			return nil
		}
		lastID = page.lastID
	}
}

// DeleteIDs removes exactly the caller's IDs. Unknown IDs are ignored.
func (s *Store) DeleteIDs(ctx context.Context, ids []string) error {
	args := make([]any, len(ids))
	for index, id := range ids {
		args[index] = []byte(id)
	}
	return s.deleteIDs(ctx, nil, args)
}

func (s *Store) deleteIDs(ctx context.Context, transaction *sql.Tx, ids []any) error {
	if len(ids) == 0 {
		return nil
	}
	statement := "DELETE FROM " + s.fullTable + " WHERE " + s.idColumn + " IN (" + strings.Repeat("?, ", len(ids)-1) + "?)"
	var err error
	if transaction == nil {
		_, err = s.db.ExecContext(ctx, statement, ids...)
	} else {
		_, err = transaction.ExecContext(ctx, statement, ids...)
	}
	if err != nil {
		return fmt.Errorf("mariadb: delete by IDs from %s: %w", s.fullTable, err)
	}
	return nil
}

const filterPageSize = 512

type filterPage struct {
	lastID []byte
	ids    []any
	count  int
}

func (s *Store) readFilterPage(ctx context.Context, transaction *sql.Tx, predicate filter.Predicate, lastID []byte) (page filterPage, err error) {
	statement := "SELECT " + s.idColumn + ", " + s.metadataColumn + " FROM " + s.fullTable + " FORCE INDEX(PRIMARY)"
	var args []any
	if lastID != nil {
		statement += " WHERE " + s.idColumn + " > ?"
		args = append(args, lastID)
	}
	statement += " ORDER BY " + s.idColumn + " ASC LIMIT ?"
	args = append(args, filterPageSize)
	rows, err := transaction.QueryContext(ctx, statement, args...)
	if err != nil {
		return page, fmt.Errorf("mariadb: read filter metadata: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	for rows.Next() {
		var id []byte
		var raw sql.NullString
		if err := rows.Scan(&id, &raw); err != nil {
			return page, fmt.Errorf("mariadb: scan filter metadata: %w", err)
		}
		var facts metadata.Map
		if raw.Valid {
			if err := jsonv2.Unmarshal([]byte(raw.String), &facts); err != nil {
				return page, fmt.Errorf("mariadb: decode filter metadata: %w", err)
			}
		}
		values, err := facts.Values()
		if err != nil {
			return page, fmt.Errorf("mariadb: decode filter values: %w", err)
		}
		match, err := filter.Match(predicate, values)
		if err != nil {
			return page, fmt.Errorf("mariadb: evaluate filter: %w", err)
		}
		page.lastID = id
		page.count++
		if match {
			page.ids = append(page.ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		return page, fmt.Errorf("mariadb: read filter metadata: %w", err)
	}
	return page, nil
}

// Validate every row before embedding or deletion. Paging bounds wire arguments
// and retained IDs; a second pass projects matches from the same transaction.
// Serializable reads prevent DELETE's current reads from racing that snapshot.
func (s *Store) prepareFilter(ctx context.Context, predicate filter.Predicate, readOnly bool) (*sql.Tx, error) {
	if predicate == nil {
		return nil, nil
	}
	isolation := sql.LevelSerializable
	if readOnly {
		isolation = sql.LevelRepeatableRead
	}
	transaction, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: isolation, ReadOnly: readOnly})
	if err != nil {
		return nil, fmt.Errorf("mariadb: begin filter transaction: %w", err)
	}
	var lastID []byte
	for {
		page, pageErr := s.readFilterPage(ctx, transaction, predicate, lastID)
		if pageErr != nil {
			return transaction, pageErr
		}
		if page.count < filterPageSize {
			return transaction, nil
		}
		lastID = page.lastID
	}
}

func finishTransaction(transaction *sql.Tx, err *error) {
	if transaction == nil {
		return
	}
	if *err == nil {
		if commitErr := transaction.Commit(); commitErr != nil {
			*err = fmt.Errorf("mariadb: commit filter transaction: %w", commitErr)
		}
		return
	}
	if rollbackErr := transaction.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
		*err = errors.Join(*err, fmt.Errorf("mariadb: rollback filter transaction: %w", rollbackErr))
	}
}
