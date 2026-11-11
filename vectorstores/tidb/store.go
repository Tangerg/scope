package tidb

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

const Provider = "TiDB"

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

// The metric is a closed vocabulary because score direction and threshold
// semantics depend on it: the same raw number means "near" under one metric and
// "far" under another, so an unrecognized value must be rejected rather than
// guessed.
const (
	DistanceCosine     DistanceMetric = "COSINE"
	DistanceL2         DistanceMetric = "L2"
	DistanceNegativeIP DistanceMetric = "NEGATIVE_INNER_PRODUCT"
)

func (d DistanceMetric) Valid() bool {
	switch d {
	case DistanceCosine, DistanceL2, DistanceNegativeIP:
		return true
	default:
		return false
	}
}

func (d DistanceMetric) String() string { return string(d) }

func (d DistanceMetric) function() string {
	switch d {
	case DistanceL2:
		return "VEC_L2_DISTANCE"
	case DistanceNegativeIP:
		return "VEC_NEGATIVE_INNER_PRODUCT"
	case DistanceCosine:
		fallthrough
	default:
		return "VEC_COSINE_DISTANCE"
	}
}

func (d DistanceMetric) score(distance float64) vectorstore.Score {
	switch d {
	case DistanceL2:
		return vectorstore.ScoreFromDistance(distance)
	case DistanceNegativeIP:
		return vectorstore.ScoreFromNegativeInnerProductDistance(distance)
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
		return "", fmt.Errorf("%w: tidb float32 vector: %w", vectorstore.ErrInvalidRequest, err)
	}
	if d == DistanceCosine {
		// TiDB accumulates squared norms in float32. Finite, nonzero
		// coordinates can still underflow or overflow into NULL distances.
		var squaredNorm float32
		for _, value := range vector {
			squaredNorm += value * value
		}
		if squaredNorm <= 0 || math.IsInf(float64(squaredNorm), 0) {
			return "", fmt.Errorf("%w: tidb cosine vector requires a finite positive float32 squared norm", vectorstore.ErrInvalidRequest)
		}
	}
	encoded, err := jsonv2.Marshal(vector)
	if err != nil {
		return "", fmt.Errorf("%w: tidb encode vector: %w", vectorstore.ErrInvalidRequest, err)
	}
	return string(encoded), nil
}

type StoreConfig struct {
	// DB must be a *sql.DB built from github.com/go-sql-driver/mysql pointed
	// at a TiDB v8.4+ cluster.
	DB *sql.DB

	SchemaName      string
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
		return errors.New("tidb: DB is required")
	}
	if err := s.Space.Validate(); err != nil {
		return err
	}
	if s.Dimensions < 0 {
		return errors.New("tidb: Dimensions must be >= 0")
	}
	if !s.DistanceMetric.Valid() {
		return fmt.Errorf("tidb: unsupported DistanceMetric %q", s.DistanceMetric)
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

// NewStore creates the schema when requested and verifies the current storage
// contract. The caller owns the database handle.
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
		return nil, fmt.Errorf("tidb: initialize store: %w", err)
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
		return s.validateSchema(ctx)
	}
	if s.dimensions <= 0 {
		return errors.New("tidb: Dimensions must be > 0")
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
	return s.validateSchema(ctx)
}

func (s *Store) validateSchema(ctx context.Context) (err error) {
	const columnNotNullable = "NO"
	const database = "COALESCE(NULLIF(?, ''), DATABASE())"
	var dataType, nullable string
	var length sql.NullInt64
	err = s.db.QueryRowContext(ctx,
		"SELECT c.DATA_TYPE, c.CHARACTER_MAXIMUM_LENGTH, c.IS_NULLABLE FROM information_schema.COLUMNS AS c WHERE c.TABLE_SCHEMA = "+database+" AND c.TABLE_NAME = ? AND c.COLUMN_NAME = ?",
		string(s.schemaName), string(s.tableName), string(s.idColumn)).Scan(&dataType, &length, &nullable)
	if err != nil {
		return fmt.Errorf("read document ID schema: %w", err)
	}
	if dataType != "varbinary" || !length.Valid || length.Int64 != documentIDBytes || nullable != columnNotNullable {
		return fmt.Errorf("document ID column must be VARBINARY(%d) NOT NULL; rebuild the table", documentIDBytes)
	}
	err = s.db.QueryRowContext(ctx,
		"SELECT DATA_TYPE, IS_NULLABLE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = "+database+" AND TABLE_NAME = ? AND COLUMN_NAME = ?",
		string(s.schemaName), string(s.tableName), string(s.metadataColumn)).Scan(&dataType, &nullable)
	if err != nil {
		return fmt.Errorf("read metadata schema: %w", err)
	}
	if dataType != "longblob" || nullable != columnNotNullable {
		return errors.New("metadata column must be LONGBLOB NOT NULL to retain encoded JSON; rebuild the table")
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

// TiFlash ANN ranking is approximate. The TiKV primary table and exact ID
// ordering keep a search projection from changing document visibility.
func (s *Store) searchStatement(wherePart string) string {
	return fmt.Sprintf(
		`SELECT /*+ READ_FROM_STORAGE(TIKV[source]) */ %s, %s, %s, %s(%s, VEC_FROM_TEXT(?)) AS distance `+
			`FROM %s AS source FORCE INDEX(PRIMARY) WHERE 1=1%s ORDER BY distance ASC, %s ASC LIMIT ?`,
		s.idColumn.sql(), s.contentColumn.sql(), s.metadataColumn.sql(),
		s.distanceMetric.function(), s.embeddingColumn.sql(),
		s.tableSQL(), wherePart, s.idColumn.sql(),
	)
}

func (s *Store) createTableStatement() string {
	return fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %s (
			%s VARBINARY(%d) NOT NULL PRIMARY KEY,
			%s TEXT,
			%s LONGBLOB NOT NULL,
			%s VECTOR(%d) NOT NULL
		) ENGINE=InnoDB DEFAULT CHARACTER SET %s COMMENT='%s'`,
		s.tableSQL(),
		s.idColumn.sql(), documentIDBytes,
		s.contentColumn.sql(),
		s.metadataColumn.sql(),
		s.embeddingColumn.sql(), s.dimensions, nativeTextCharset, s.space.StorageKey(),
	)
}

func (s *Store) binding(ctx context.Context) (nativeBinding, error) {
	var marker, shape, contentCharset string
	var contentType nativeTextType
	const query = `SELECT t.TABLE_COMMENT, v.COLUMN_TYPE, c.DATA_TYPE, c.CHARACTER_SET_NAME
FROM information_schema.TABLES t
JOIN information_schema.COLUMNS v ON v.TABLE_SCHEMA = t.TABLE_SCHEMA AND v.TABLE_NAME = t.TABLE_NAME
JOIN information_schema.COLUMNS c ON c.TABLE_SCHEMA = t.TABLE_SCHEMA AND c.TABLE_NAME = t.TABLE_NAME
WHERE t.TABLE_SCHEMA = COALESCE(NULLIF(?, ''), DATABASE()) AND t.TABLE_NAME = ?
AND v.COLUMN_NAME = ? AND c.COLUMN_NAME = ?`
	err := s.db.QueryRowContext(ctx, query, string(s.schemaName), string(s.tableName), string(s.embeddingColumn), string(s.contentColumn)).Scan(&marker, &shape, &contentType, &contentCharset)
	if err != nil {
		return nativeBinding{}, err
	}
	if marker != s.space.StorageKey() {
		return nativeBinding{}, vectorstore.ErrIncompatibleSpace
	}
	contentBytes, err := contentType.byteCapacity()
	if err != nil {
		return nativeBinding{}, err
	}
	if contentCharset != nativeTextCharset {
		return nativeBinding{}, errors.New("tidb: content column requires utf8mb4")
	}
	width, ok := strings.CutPrefix(strings.ToLower(shape), "vector(")
	if !ok {
		return nativeBinding{}, errors.New("tidb: requires a fixed native vector width")
	}
	width, ok = strings.CutSuffix(width, ")")
	if !ok {
		return nativeBinding{}, errors.New("tidb: invalid native vector type")
	}
	dim, err := strconv.Atoi(width)
	if err != nil || dim <= 0 {
		return nativeBinding{}, errors.New("tidb: invalid native vector width")
	}
	return nativeBinding{dimensions: dim, contentBytes: contentBytes}, nil
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
		if err := binding.checkContent(d.Text); err != nil {
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
		if contentErr := binding.checkContent(doc.Text); contentErr != nil {
			return contentErr
		}
		ids[i] = doc.ID
		metaJSON, encodeErr := doc.MarshalStoredMetadata()
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
		`INSERT INTO %s (%s, %s, %s, %s) VALUES (?, ?, ?, ?) `+
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
				execErr = errors.New("tidb: missing native row acknowledgment")
			} else {
				var count int64
				count, execErr = result.RowsAffected()
				if execErr == nil && (count < 0 || count > 2) {
					execErr = fmt.Errorf("tidb: invalid upsert acknowledgment %d", count)
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

func (s *Store) rank(ctx context.Context, request *vectorstore.Query, budget *vectorstore.ScanBudget) (response *vectorstore.SearchResponse, err error) {
	if err = request.ValidateFor(s.space); err != nil {
		return nil, fmt.Errorf("tidb: rank query: %w", err)
	}
	if err = request.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, fmt.Errorf("tidb: rank query: %w", err)
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
		transaction, err = s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
		if err != nil {
			return nil, fmt.Errorf("tidb: begin filter transaction: %w", err)
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
			if len(page.matches) > 0 {
				ids := make([]any, len(page.matches))
				for index, match := range page.matches {
					ids[index] = match.id
				}
				candidates, queryErr := s.searchRows(ctx, transaction, request, encoded, ids)
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
			return nil, fmt.Errorf("tidb: commit filter transaction: %w", err)
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
		return nil, fmt.Errorf("tidb: query %s: %w", s.tableSQL(), err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	for rows.Next() {
		var id string
		var content sql.NullString
		var raw []byte
		var distance float64
		if err := rows.Scan(&id, &content, &raw, &distance); err != nil {
			return nil, fmt.Errorf("tidb: scan row: %w", err)
		}
		score := s.distanceMetric.score(distance)
		if err := score.Validate(); err != nil {
			return nil, fmt.Errorf("tidb: distance for %q: %w", id, err)
		}
		doc := &document.Document{ID: id, Text: content.String}
		if err := doc.UnmarshalStoredMetadata(raw); err != nil {
			return nil, fmt.Errorf("tidb: unmarshal metadata for %s: %w", id, err)
		}
		if err := vectorstore.ValidateDocument(doc); err != nil {
			return nil, fmt.Errorf("tidb: invalid native document %q: %w", id, err)
		}
		if score < request.Options.MinScore {
			continue
		}
		results = append(results, rankedResult{result: &vectorstore.SearchResult{Document: doc, Score: score}, distance: distance})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tidb: read rows: %w", err)
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
		return fmt.Errorf("tidb.Store.DeleteMatching: %w", validateErr)
	}
	transaction, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return fmt.Errorf("tidb: begin filter transaction: %w", err)
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
		if deleteErr := s.deleteMatches(ctx, transaction, page.matches); deleteErr != nil {
			return deleteErr
		}
		if page.count < filterPageSize {
			break
		}
		lastID = page.lastID
	}
	if err = transaction.Commit(); err != nil {
		return fmt.Errorf("tidb: commit filter transaction: %w", err)
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
	if len(args) == 0 {
		return nil
	}
	statement := "DELETE FROM " + s.tableSQL() + " WHERE " + s.idColumn.sql() + " IN (" + strings.Repeat("?, ", len(args)-1) + "?)"
	if _, err := s.db.ExecContext(ctx, statement, args...); err != nil {
		return fmt.Errorf("tidb: delete by IDs from %s: %w", s.tableSQL(), err)
	}
	return nil
}

const filterPageSize = 512

type filterMatch struct {
	id       []byte
	metadata []byte
}

type filterPage struct {
	lastID  []byte
	matches []filterMatch
	count   int
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
		return page, fmt.Errorf("tidb: read filter metadata: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	for rows.Next() {
		var id []byte
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return page, fmt.Errorf("tidb: scan filter metadata: %w", err)
		}
		fields := new(document.Document)
		if err := fields.UnmarshalStoredMetadata(raw); err != nil {
			return page, fmt.Errorf("tidb: decode filter metadata: %w", err)
		}
		match, err := filter.Match(predicate, fields.Metadata)
		if err != nil {
			return page, fmt.Errorf("tidb: evaluate filter: %w", err)
		}
		page.lastID = id
		page.count++
		if match {
			page.matches = append(page.matches, filterMatch{id: id, metadata: raw})
		}
	}
	if err := rows.Err(); err != nil {
		return page, fmt.Errorf("tidb: read filter metadata: %w", err)
	}
	return page, nil
}

// Validate before embedding or deletion. Both passes read one snapshot, and
// deletion compares its original bytes against TiDB's current-read DML values.
// TiDB rejects the read-only modifier; Search executes only reads in this tx.
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

func (s *Store) deleteMatches(ctx context.Context, transaction *sql.Tx, matches []filterMatch) error {
	if len(matches) == 0 {
		return nil
	}
	conditions := make([]string, len(matches))
	args := make([]any, 0, 2*len(matches))
	for index, match := range matches {
		conditions[index] = "(" + s.idColumn.sql() + " = ? AND " + s.metadataColumn.sql() + " = ?)"
		args = append(args, match.id, match.metadata)
	}
	result, err := transaction.ExecContext(ctx, "DELETE FROM "+s.tableSQL()+" WHERE "+strings.Join(conditions, " OR "), args...)
	if err != nil {
		return fmt.Errorf("tidb: delete matching documents: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("tidb: read matching deletion count: %w", err)
	}
	if count != int64(len(matches)) {
		return fmt.Errorf("tidb: metadata changed during filtered deletion: deleted %d of %d snapshot documents", count, len(matches))
	}
	return nil
}

func rollbackTransaction(transaction *sql.Tx, err *error) {
	if rollbackErr := transaction.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
		*err = errors.Join(*err, fmt.Errorf("tidb: rollback filter transaction: %w", rollbackErr))
	}
}
