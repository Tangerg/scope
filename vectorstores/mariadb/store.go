package mariadb

import (
	"cmp"
	"context"
	"database/sql"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

const Provider = "MariaDB"

const documentIDBytes = 3072

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
	DistanceCosine DistanceMetric = "cosine"

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

type StoreConfig struct {
	// DB must be a *sql.DB built from the github.com/go-sql-driver/mysql
	// driver pointed at a MariaDB 11.7+ instance (or Enterprise Server
	// 11.4.5-3+) with vector support.
	DB *sql.DB

	SchemaName string

	TableName string

	IDColumn        string
	ContentColumn   string
	MetadataColumn  string
	EmbeddingColumn string

	Space vectorstore.SpaceID

	// Dimensions is required when InitializeSchema is true: the width is part
	// of the VECTOR column type and cannot be read off a table that does not
	// exist yet.
	Dimensions int

	DistanceMetric DistanceMetric

	InitializeSchema bool
}

func (s StoreConfig) Validate() error {
	s.applyDefaults()
	if s.DB == nil {
		return errors.New("mariadb: DB is required")
	}
	if err := s.Space.Validate(); err != nil {
		return err
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
	schemaName      string
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

// NewStore creates the schema when requested and verifies that the table's
// constraints preserve exact document IDs. The caller owns the database handle.
func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	config.applyDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}

	var err error

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
		space:           config.Space,
		dimensions:      config.Dimensions,
		distanceMetric:  config.DistanceMetric,
	}

	if err = store.initialize(ctx, config.InitializeSchema); err != nil {
		return nil, fmt.Errorf("mariadb: initialize store: %w", err)
	}
	if _, err := store.binding(ctx); err != nil {
		return nil, err
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
		) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COMMENT='%s'`,
		s.fullTable,
		s.idColumn, documentIDBytes,
		s.contentColumn,
		s.metadataColumn,
		s.embeddingColumn, s.dimensions, s.space.StorageKey(),
	)
}

func (s *Store) binding(ctx context.Context) (int, error) {
	var marker, shape string
	err := s.db.QueryRowContext(ctx, `SELECT t.TABLE_COMMENT,c.COLUMN_TYPE FROM information_schema.TABLES t JOIN information_schema.COLUMNS c ON c.TABLE_SCHEMA=t.TABLE_SCHEMA AND c.TABLE_NAME=t.TABLE_NAME WHERE t.TABLE_SCHEMA=COALESCE(NULLIF(?,''),DATABASE()) AND t.TABLE_NAME=? AND c.COLUMN_NAME=?`, s.schemaName, s.tableName, s.embeddingColumn).Scan(&marker, &shape)
	if err != nil {
		return 0, err
	}
	if marker != s.space.StorageKey() {
		return 0, vectorstore.ErrIncompatibleSpace
	}
	width, ok := strings.CutPrefix(strings.ToLower(shape), "vector(")
	if !ok {
		return 0, errors.New("mariadb: requires a fixed native vector width")
	}
	width, ok = strings.CutSuffix(width, ")")
	if !ok {
		return 0, errors.New("mariadb: invalid native vector type")
	}
	dim, err := strconv.Atoi(width)
	if err != nil || dim <= 0 {
		return 0, errors.New("mariadb: invalid native vector width")
	}
	return dim, nil
}
func (s *Store) checkDocument(d *document.Document) error {
	if len(d.ID) > documentIDBytes || d.Media != nil {
		return fmt.Errorf("%w: unsupported document identity or media", vectorstore.ErrInvalidDocument)
	}
	return nil
}
func (s *Store) CheckDocuments(ctx context.Context, space vectorstore.SpaceID, docs []*document.Document) error {
	if err := space.Require(s.space); err != nil {
		return err
	}
	if err := vectorstore.ValidateDocuments(docs); err != nil {
		return err
	}
	for _, d := range docs {
		if err := s.checkDocument(d); err != nil {
			return err
		}
	}
	_, err := s.binding(ctx)
	return err
}
func (s *Store) Upsert(ctx context.Context, batch *vectorstore.Batch) (err error) {
	if batchErr := batch.ValidateFor(s.space); batchErr != nil {
		return batchErr
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	for _, r := range batch.Records {
		if docErr := s.checkDocument(r.Document); docErr != nil {
			return docErr
		}
	}
	width, err := s.binding(ctx)
	if err != nil {
		return err
	}
	if len(batch.Records[0].Values) != width {
		return fmt.Errorf("%w: native vector width is %d", vectorstore.ErrInvalidRequest, width)
	}
	args := make([][]any, len(batch.Records))
	ids := make([]string, len(args))
	for i, r := range batch.Records {
		doc := r.Document
		ids[i] = doc.ID
		metaJSON, encodeErr := doc.MarshalStoredMetadata()
		if encodeErr != nil {
			return encodeErr
		}
		vectorJSON, encodeErr := jsonv2.Marshal(embedding.Float32Vector(r.Values))
		if encodeErr != nil {
			return encodeErr
		}
		args[i] = []any{[]byte(doc.ID), doc.Text, metaJSON, string(vectorJSON)}
	}
	upsert := fmt.Sprintf(
		`INSERT INTO %s (%s, %s, %s, %s) VALUES (?, ?, ?, VEC_FromText(?)) `+
			`ON DUPLICATE KEY UPDATE %s = VALUES(%s), %s = VALUES(%s), %s = VALUES(%s)`,
		s.fullTable, s.idColumn, s.contentColumn, s.metadataColumn, s.embeddingColumn,
		s.contentColumn, s.contentColumn,
		s.metadataColumn, s.metadataColumn,
		s.embeddingColumn, s.embeddingColumn,
	)
	stmt, err := s.db.PrepareContext(ctx, upsert)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := stmt.Close(); closeErr != nil {
			if err == nil {
				err = &vectorstore.WriteError{Err: closeErr, Acknowledged: ids}
			} else {
				err = errors.Join(err, closeErr)
			}
		}
	}()
	for i, row := range args {
		if err := ctx.Err(); err != nil {
			return (vectorstore.WriteError{Err: err, Acknowledged: ids[:i], Unattempted: ids[i:]}).Clone()
		}
		result, execErr := stmt.ExecContext(ctx, row...)
		if execErr == nil {
			if result == nil {
				execErr = errors.New("mariadb: missing native row acknowledgment")
			} else {
				var count int64
				count, execErr = result.RowsAffected()
				if execErr == nil && (count < 0 || count > 2) {
					execErr = fmt.Errorf("mariadb: invalid upsert acknowledgment %d", count)
				}
			}
		}
		if err := execErr; err != nil {
			return (vectorstore.WriteError{Err: err, Acknowledged: ids[:i], Unconfirmed: ids[i : i+1], Unattempted: ids[i+1:]}).Clone()
		}
	}
	return nil
}

func (s *Store) CheckQuery(ctx context.Context, space vectorstore.SpaceID, opts vectorstore.SearchOptions) error {
	if err := space.Require(s.space); err != nil {
		return err
	}
	if err := opts.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return err
	}
	if opts.Filter != nil {
		return vectorstore.ErrUnsupportedFilter
	}
	_, err := s.binding(ctx)
	return err
}
func (s *Store) Query(ctx context.Context, q *vectorstore.Query) (*vectorstore.SearchResponse, error) {
	if err := q.ValidateFor(s.space); err != nil {
		return nil, err
	}
	if q.Options.Filter != nil {
		return nil, vectorstore.ErrUnsupportedFilter
	}
	return s.rank(ctx, q, nil)
}
func (s *Store) QueryByScan(ctx context.Context, q *vectorstore.Query, budget vectorstore.ScanBudget) (*vectorstore.SearchResponse, error) {
	if err := q.ValidateFor(s.space); err != nil {
		return nil, err
	}
	if q.Options.Filter == nil {
		return nil, vectorstore.ErrMissingFilter
	}
	if err := budget.Validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, budget.Timeout)
	defer cancel()
	return s.rank(ctx, q, &budget)
}
func (s *Store) rank(ctx context.Context, request *vectorstore.Query, budget *vectorstore.ScanBudget) (response *vectorstore.SearchResponse, err error) {
	if err = request.ValidateFor(s.space); err != nil {
		return nil, fmt.Errorf("mariadb: rank query: %w", err)
	}
	if err = request.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, fmt.Errorf("mariadb: rank query: %w", err)
	}
	defer func() {
		if err == nil {
			err = request.ValidateResponse(response)
		}
		if err != nil {
			response = nil
		}
	}()
	width, err := s.binding(ctx)
	if err != nil {
		return nil, err
	}
	if len(request.Values) != width {
		return nil, vectorstore.ErrInvalidRequest
	}
	transaction, err := s.prepareFilter(ctx, request.Options.Filter, true, budget)
	defer finishTransaction(transaction, &err)
	if err != nil {
		return nil, err
	}

	encoded, err := jsonv2.Marshal(embedding.Float32Vector(request.Values))
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

func (s *Store) searchRows(ctx context.Context, transaction *sql.Tx, request *vectorstore.Query, vector string, ids []any) (results []rankedResult, err error) {
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
			if err := doc.UnmarshalStoredMetadata([]byte(raw.String)); err != nil {
				return nil, fmt.Errorf("mariadb: unmarshal metadata for %s: %w", id, err)
			}
		}
		if err := vectorstore.ValidateDocument(doc); err != nil {
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

func (s *Store) DeleteMatching(ctx context.Context, predicate filter.Predicate, budget vectorstore.ScanBudget) (err error) {
	if budgetErr := budget.Validate(); budgetErr != nil {
		return budgetErr
	}
	ctx, cancel := context.WithTimeout(ctx, budget.Timeout)
	defer cancel()
	if _, bindingErr := s.binding(ctx); bindingErr != nil {
		return bindingErr
	}
	if predicate == nil {
		return vectorstore.ErrMissingFilter
	}
	if validateErr := predicate.Validate(); validateErr != nil {
		return fmt.Errorf("mariadb.Store.DeleteMatching: %w", validateErr)
	}
	transaction, err := s.prepareFilter(ctx, predicate, false, &budget)
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
		fields := new(document.Document)
		if raw.Valid {
			if err := fields.UnmarshalStoredMetadata([]byte(raw.String)); err != nil {
				return page, fmt.Errorf("mariadb: decode filter metadata: %w", err)
			}
		}
		values, err := fields.Metadata.Values()
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
func (s *Store) prepareFilter(ctx context.Context, predicate filter.Predicate, readOnly bool, budget *vectorstore.ScanBudget) (*sql.Tx, error) {
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
	if budget == nil {
		return transaction, vectorstore.ErrUnsupportedFilter
	}
	var allowed int
	statement := fmt.Sprintf("SELECT CASE WHEN COUNT(*)<=? AND 2*COALESCE(SUM(n),0)<=? THEN 1 ELSE 0 END FROM (SELECT OCTET_LENGTH(%s)+COALESCE(OCTET_LENGTH(%s),0) AS n FROM %s LIMIT ?) AS bounded", s.idColumn, s.metadataColumn, s.fullTable)
	if err := transaction.QueryRowContext(ctx, statement, budget.MaxRecords, budget.MaxBytes, budget.MaxRecords+1).Scan(&allowed); err != nil {
		return transaction, err
	}
	if allowed != 1 {
		return transaction, vectorstore.ErrScanBudget
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
