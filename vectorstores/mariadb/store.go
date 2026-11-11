package mariadb

import (
	"cmp"
	"context"
	"database/sql"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/Tangerg/scope/core/document"
	corevector "github.com/Tangerg/scope/core/vector"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

const Provider = "MariaDB"

const (
	documentIDBytes       = 3072
	requiredStorageEngine = "InnoDB"
)

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

func (d DistanceMetric) encodeVector(values []float64, width int) (string, error) {
	if len(values) != width {
		return "", fmt.Errorf("%w: native vector width is %d", vectorstore.ErrInvalidRequest, width)
	}
	vector := corevector.Float32(values)
	if err := corevector.Coordinates[float32](vector).Validate(); err != nil {
		return "", fmt.Errorf("%w: mariadb float32 vector: %w", vectorstore.ErrInvalidRequest, err)
	}
	// VECTOR and VEC_FromText reject nonfinite float32 squared norms for
	// every metric. Cosine also needs a positive norm for a defined distance.
	var squaredNorm float32
	for _, value := range vector {
		squaredNorm += value * value
	}
	if math.IsInf(float64(squaredNorm), 0) {
		return "", fmt.Errorf("%w: mariadb vector requires a finite float32 squared norm", vectorstore.ErrInvalidRequest)
	}
	if d == DistanceCosine && squaredNorm == 0 {
		return "", fmt.Errorf("%w: mariadb cosine vector requires a positive float32 squared norm", vectorstore.ErrInvalidRequest)
	}
	encoded, err := jsonv2.Marshal(vector)
	if err != nil {
		return "", fmt.Errorf("%w: mariadb encode vector: %w", vectorstore.ErrInvalidRequest, err)
	}
	return string(encoded), nil
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
	columns := [...]struct {
		role string
		name string
	}{
		{role: "IDColumn", name: s.IDColumn},
		{role: "ContentColumn", name: s.ContentColumn},
		{role: "MetadataColumn", name: s.MetadataColumn},
		{role: "EmbeddingColumn", name: s.EmbeddingColumn},
	}
	owners := make(map[string]string, len(columns))
	for _, column := range columns {
		if err := identifier(column.name).validate(column.role); err != nil {
			return err
		}
		// MariaDB column identity is case insensitive, even when quoted.
		identity := strings.ToLower(column.name)
		if owner, exists := owners[identity]; exists {
			return fmt.Errorf("mariadb: %s and %s must name distinct columns", owner, column.role)
		}
		owners[identity] = column.role
	}
	return nil
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
	schemaName      identifier
	tableName       identifier
	idColumn        identifier
	contentColumn   identifier
	metadataColumn  identifier
	embeddingColumn identifier
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

	store := &Store{
		db:              config.DB,
		schemaName:      identifier(config.SchemaName),
		tableName:       identifier(config.TableName),
		idColumn:        identifier(config.IDColumn),
		contentColumn:   identifier(config.ContentColumn),
		metadataColumn:  identifier(config.MetadataColumn),
		embeddingColumn: identifier(config.EmbeddingColumn),
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

func (s *Store) tableSQL() string {
	if s.schemaName == "" {
		return s.tableName.sql()
	}
	return s.schemaName.sql() + "." + s.tableName.sql()
}

func (s *Store) initialize(ctx context.Context, initSchema bool) error {
	if !initSchema {
		return s.validateIdentitySchema(ctx)
	}
	if s.dimensions <= 0 {
		return errors.New("mariadb: Dimensions must be > 0")
	}

	if s.schemaName != "" {
		if _, err := s.db.ExecContext(ctx,
			fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", s.schemaName.sql())); err != nil {
			return fmt.Errorf("create schema %s: %w", s.schemaName.sql(), err)
		}
	}

	if _, err := s.db.ExecContext(ctx, s.createTableStatement()); err != nil {
		return fmt.Errorf("create table %s: %w", s.tableSQL(), err)
	}
	return s.validateIdentitySchema(ctx)
}

func (s *Store) validateIdentitySchema(ctx context.Context) (err error) {
	const database = "COALESCE(NULLIF(?, ''), DATABASE())"
	const columnNotNullable = "NO"
	var dataType, nullable, engine string
	var length sql.NullInt64
	err = s.db.QueryRowContext(ctx,
		"SELECT c.DATA_TYPE, c.CHARACTER_MAXIMUM_LENGTH, c.IS_NULLABLE, t.ENGINE FROM information_schema.COLUMNS AS c JOIN information_schema.TABLES AS t ON t.TABLE_SCHEMA = c.TABLE_SCHEMA AND t.TABLE_NAME = c.TABLE_NAME WHERE c.TABLE_SCHEMA = "+database+" AND c.TABLE_NAME = ? AND c.COLUMN_NAME = ?",
		string(s.schemaName), string(s.tableName), string(s.idColumn)).Scan(&dataType, &length, &nullable, &engine)
	if err != nil {
		return fmt.Errorf("read document ID schema: %w", err)
	}
	if dataType != "varbinary" || !length.Valid || length.Int64 != documentIDBytes || nullable != columnNotNullable {
		return fmt.Errorf("document ID column must be VARBINARY(%d) NOT NULL; rebuild the table", documentIDBytes)
	}
	if engine != requiredStorageEngine {
		return errors.New("filter transactions require an InnoDB table; rebuild the table")
	}
	rows, err := s.db.QueryContext(ctx,
		"SELECT INDEX_NAME, COLUMN_NAME, SEQ_IN_INDEX, SUB_PART FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = "+database+" AND TABLE_NAME = ? AND NON_UNIQUE = 0",
		string(s.schemaName), string(s.tableName))
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
		if !strings.EqualFold(column, string(s.idColumn)) || ordinal != 1 || prefix.Valid {
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
		s.idColumn.sql(), s.contentColumn.sql(), s.metadataColumn.sql(),
		s.distanceMetric, s.embeddingColumn.sql(),
		s.tableSQL(), wherePart, s.idColumn.sql(),
	)
}

func (s *Store) createTableStatement() string {
	return fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %s (
			%s VARBINARY(%d) NOT NULL PRIMARY KEY,
			%s TEXT,
			%s JSON,
			%s VECTOR(%d) NOT NULL
		) ENGINE=%s DEFAULT CHARACTER SET %s COMMENT='%s'`,
		s.tableSQL(),
		s.idColumn.sql(), documentIDBytes,
		s.contentColumn.sql(),
		s.metadataColumn.sql(),
		s.embeddingColumn.sql(), s.dimensions, requiredStorageEngine, nativeTextCharset, s.space.StorageKey(),
	)
}

func (s *Store) binding(ctx context.Context) (nativeBinding, error) {
	var marker, shape string
	binding := nativeBinding{content: nativeTextColumn{name: string(s.contentColumn)}, metadata: nativeTextColumn{name: string(s.metadataColumn)}}
	const query = `SELECT t.TABLE_COMMENT, v.COLUMN_TYPE,
c.DATA_TYPE, c.CHARACTER_SET_NAME, c.CHARACTER_OCTET_LENGTH,
m.DATA_TYPE, m.CHARACTER_SET_NAME, m.CHARACTER_OCTET_LENGTH
FROM information_schema.TABLES t
JOIN information_schema.COLUMNS v ON v.TABLE_SCHEMA = t.TABLE_SCHEMA AND v.TABLE_NAME = t.TABLE_NAME
JOIN information_schema.COLUMNS c ON c.TABLE_SCHEMA = t.TABLE_SCHEMA AND c.TABLE_NAME = t.TABLE_NAME
JOIN information_schema.COLUMNS m ON m.TABLE_SCHEMA = t.TABLE_SCHEMA AND m.TABLE_NAME = t.TABLE_NAME
WHERE t.TABLE_SCHEMA = COALESCE(NULLIF(?, ''), DATABASE()) AND t.TABLE_NAME = ?
AND v.COLUMN_NAME = ? AND c.COLUMN_NAME = ? AND m.COLUMN_NAME = ?`
	err := s.db.QueryRowContext(ctx, query, string(s.schemaName), string(s.tableName), string(s.embeddingColumn), string(s.contentColumn), string(s.metadataColumn)).Scan(
		&marker, &shape,
		&binding.content.dataType, &binding.content.charset, &binding.content.maxBytes,
		&binding.metadata.dataType, &binding.metadata.charset, &binding.metadata.maxBytes,
	)
	if err != nil {
		return nativeBinding{}, err
	}
	if marker != s.space.StorageKey() {
		return nativeBinding{}, vectorstore.ErrIncompatibleSpace
	}
	if err = binding.content.validate(); err != nil {
		return nativeBinding{}, err
	}
	if err = binding.metadata.validate(); err != nil {
		return nativeBinding{}, err
	}
	width, ok := strings.CutPrefix(strings.ToLower(shape), "vector(")
	if !ok {
		return nativeBinding{}, errors.New("mariadb: requires a fixed native vector width")
	}
	width, ok = strings.CutSuffix(width, ")")
	if !ok {
		return nativeBinding{}, errors.New("mariadb: invalid native vector type")
	}
	dim, err := strconv.Atoi(width)
	if err != nil || dim <= 0 {
		return nativeBinding{}, errors.New("mariadb: invalid native vector width")
	}
	binding.dimensions = dim
	return binding, nil
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
	binding, err := s.binding(ctx)
	if err != nil {
		return err
	}
	for _, d := range docs {
		if err := binding.content.checkBytes(len(d.Text)); err != nil {
			return err
		}
		if _, err := binding.encodeMetadata(d); err != nil {
			return err
		}
	}
	return nil
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
	binding, err := s.binding(ctx)
	if err != nil {
		return err
	}
	args := make([][]any, len(batch.Records))
	ids := make([]string, len(args))
	for i, r := range batch.Records {
		doc := r.Document
		if contentErr := binding.content.checkBytes(len(doc.Text)); contentErr != nil {
			return contentErr
		}
		ids[i] = doc.ID
		metaJSON, encodeErr := binding.encodeMetadata(doc)
		if encodeErr != nil {
			return encodeErr
		}
		vectorJSON, encodeErr := s.distanceMetric.encodeVector(r.Values, binding.dimensions)
		if encodeErr != nil {
			return encodeErr
		}
		args[i] = []any{[]byte(doc.ID), doc.Text, metaJSON, vectorJSON}
	}
	upsert := fmt.Sprintf(
		`INSERT INTO %s (%s, %s, %s, %s) VALUES (?, ?, ?, VEC_FromText(?)) `+
			`ON DUPLICATE KEY UPDATE %s = VALUES(%s), %s = VALUES(%s), %s = VALUES(%s)`,
		s.tableSQL(), s.idColumn.sql(), s.contentColumn.sql(), s.metadataColumn.sql(), s.embeddingColumn.sql(),
		s.contentColumn.sql(), s.contentColumn.sql(),
		s.metadataColumn.sql(), s.metadataColumn.sql(),
		s.embeddingColumn.sql(), s.embeddingColumn.sql(),
	)
	stmt, err := s.db.PrepareContext(ctx, upsert)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := stmt.Close(); closeErr != nil {
			if err == nil {
				err = vectorstore.NewWriteError(closeErr, vectorstore.WriteOutcome{Acknowledged: ids})
			} else {
				err = errors.Join(err, closeErr)
			}
		}
	}()
	for i, row := range args {
		if err := ctx.Err(); err != nil {
			return vectorstore.NewWriteError(err, vectorstore.WriteOutcome{Acknowledged: ids[:i], Unattempted: ids[i:]})
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
			return vectorstore.NewWriteError(err, vectorstore.WriteOutcome{Acknowledged: ids[:i], Unconfirmed: ids[i : i+1], Unattempted: ids[i+1:]})
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

// rank expects a request its entry point already validated for this store.
func (s *Store) rank(ctx context.Context, request *vectorstore.Query, budget *vectorstore.ScanBudget) (response *vectorstore.SearchResponse, err error) {
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
	binding, err := s.binding(ctx)
	if err != nil {
		return nil, err
	}
	encoded, err := s.distanceMetric.encodeVector(request.Values, binding.dimensions)
	if err != nil {
		return nil, err
	}
	var transaction *sql.Tx
	if request.Options.Filter != nil {
		transaction, err = s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
		if err != nil {
			return nil, fmt.Errorf("mariadb: begin filter transaction: %w", err)
		}
		defer rollbackTransaction(transaction, &err)
		if err = s.prepareFilter(ctx, transaction, request.Options.Filter, budget); err != nil {
			return nil, err
		}
	}

	var ranked []rankedResult
	if request.Options.Filter == nil {
		ranked, err = s.searchRows(ctx, nil, request, encoded, nil)
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
				candidates, queryErr := s.searchRows(ctx, transaction, request, encoded, page.ids)
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
	if transaction != nil {
		if err = transaction.Commit(); err != nil {
			return nil, fmt.Errorf("mariadb: commit filter transaction: %w", err)
		}
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
		where = " AND " + s.idColumn.sql() + " IN (" + strings.Repeat("?, ", len(ids)-1) + "?)"
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
		return nil, fmt.Errorf("mariadb: query %s: %w", s.tableSQL(), err)
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
	transaction, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("mariadb: begin filter transaction: %w", err)
	}
	defer rollbackTransaction(transaction, &err)
	if err = s.prepareFilter(ctx, transaction, predicate, &budget); err != nil {
		return err
	}
	var lastID []byte
	for {
		page, pageErr := s.readFilterPage(ctx, transaction, predicate, lastID)
		if pageErr != nil {
			return pageErr
		}
		if deleteErr := s.deleteIDs(ctx, transaction, page.ids); deleteErr != nil {
			return deleteErr
		}
		if page.count < filterPageSize {
			break
		}
		lastID = page.lastID
	}
	if err = transaction.Commit(); err != nil {
		return fmt.Errorf("mariadb: commit filter transaction: %w", err)
	}
	return nil
}

func (s *Store) DeleteIDs(ctx context.Context, ids []string) error {
	ids, err := vectorstore.DistinctIDs(ids)
	if err != nil {
		return err
	}
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
	statement := "DELETE FROM " + s.tableSQL() + " WHERE " + s.idColumn.sql() + " IN (" + strings.Repeat("?, ", len(ids)-1) + "?)"
	var err error
	if transaction == nil {
		_, err = s.db.ExecContext(ctx, statement, ids...)
	} else {
		_, err = transaction.ExecContext(ctx, statement, ids...)
	}
	if err != nil {
		return fmt.Errorf("mariadb: delete by IDs from %s: %w", s.tableSQL(), err)
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
	statement := "SELECT " + s.idColumn.sql() + ", " + s.metadataColumn.sql() + " FROM " + s.tableSQL() + " FORCE INDEX(PRIMARY)"
	var args []any
	if lastID != nil {
		statement += " WHERE " + s.idColumn.sql() + " > ?"
		args = append(args, lastID)
	}
	statement += " ORDER BY " + s.idColumn.sql() + " ASC LIMIT ?"
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
		match, err := filter.Match(predicate, fields.Metadata)
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
func (s *Store) prepareFilter(ctx context.Context, transaction *sql.Tx, predicate filter.Predicate, budget *vectorstore.ScanBudget) error {
	if budget == nil {
		return vectorstore.ErrUnsupportedFilter
	}
	var allowed int
	statement := fmt.Sprintf("SELECT CASE WHEN COUNT(*)<=? AND 2*COALESCE(SUM(n),0)<=? THEN 1 ELSE 0 END FROM (SELECT OCTET_LENGTH(%s)+COALESCE(OCTET_LENGTH(%s),0) AS n FROM %s LIMIT ?) AS bounded", s.idColumn.sql(), s.metadataColumn.sql(), s.tableSQL())
	if err := transaction.QueryRowContext(ctx, statement, budget.MaxRecords, budget.MaxBytes, budget.MaxRecords+1).Scan(&allowed); err != nil {
		return err
	}
	if allowed != 1 {
		return vectorstore.ErrScanBudget
	}
	var lastID []byte
	for {
		page, pageErr := s.readFilterPage(ctx, transaction, predicate, lastID)
		if pageErr != nil {
			return pageErr
		}
		if page.count < filterPageSize {
			return nil
		}
		lastID = page.lastID
	}
}

func rollbackTransaction(transaction *sql.Tx, err *error) {
	if rollbackErr := transaction.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
		*err = errors.Join(*err, fmt.Errorf("mariadb: rollback filter transaction: %w", rollbackErr))
	}
}
