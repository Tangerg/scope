package oracle

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

	oratypes "github.com/sijms/go-ora/v3/types"

	"github.com/Tangerg/scope/core/document"
	corevector "github.com/Tangerg/scope/core/vector"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

const (
	documentIDBytes     = 2000
	contentCharacterSet = "AL32UTF8"
	contentColumnType   = "CLOB"
)

const Provider = "Oracle"

const (
	DefaultTableName       = "VECTOR_STORE"
	DefaultIDColumn        = "ID"
	DefaultContentColumn   = "CONTENT"
	DefaultMetadataColumn  = "METADATA"
	DefaultEmbeddingColumn = "EMBEDDING"
	DefaultDistanceMetric  = DistanceCosine
)

// DistanceMetric values mirror Oracle's accepted VECTOR_DISTANCE tokens
// exactly so they can flow straight into the SQL.
type DistanceMetric string

const (
	DistanceCosine DistanceMetric = "COSINE"

	DistanceEuclidean DistanceMetric = "EUCLIDEAN"

	// DistanceDot is Oracle's negative inner-product distance, not a bounded
	// similarity.
	DistanceDot DistanceMetric = "DOT"
)

func (d DistanceMetric) Valid() bool {
	switch d {
	case DistanceCosine, DistanceEuclidean, DistanceDot:
		return true
	default:
		return false
	}
}

func (d DistanceMetric) String() string { return string(d) }

func (d DistanceMetric) score(distance float64) vectorstore.Score {
	switch d {
	case DistanceEuclidean:
		return vectorstore.ScoreFromDistance(distance)
	case DistanceDot:
		// Oracle defines VECTOR_DISTANCE(..., DOT) as the negative inner
		// product, not as a bounded similarity.
		return vectorstore.ScoreFromNegativeInnerProductDistance(distance)
	case DistanceCosine:
		fallthrough
	default:
		return vectorstore.ScoreFromCosineDistance(distance)
	}
}

type StoreConfig struct {
	// DB must be a *sql.DB built from github.com/sijms/go-ora/v3 pointed at
	// an Oracle 23ai instance.
	DB *sql.DB

	SchemaName string

	TableName string

	IDColumn        string
	ContentColumn   string
	MetadataColumn  string
	EmbeddingColumn string

	Space vectorstore.SpaceID

	// Dimensions seeds a newly created VECTOR column; the native column owns
	// the stored dimension constraint, and runtime vectors derive width from
	// their input.
	Dimensions int

	DistanceMetric DistanceMetric

	InitializeSchema bool
}

func (s StoreConfig) Validate() error {
	s.applyDefaults()
	if s.DB == nil {
		return errors.New("oracle: DB is required")
	}
	if err := s.Space.Validate(); err != nil {
		return err
	}
	if s.Dimensions < 0 {
		return errors.New("oracle: Dimensions must be >= 0")
	}
	if s.InitializeSchema && s.Dimensions == 0 {
		return errors.New("oracle: Dimensions must be > 0 when InitializeSchema is true")
	}
	if !s.DistanceMetric.Valid() {
		return fmt.Errorf("oracle: unsupported DistanceMetric %q", s.DistanceMetric)
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

// NewStore creates the table when requested and verifies the current storage
// contract. The host owns the database handle. An omitted schema is bound to
// the connection's current schema at construction.
func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	config.applyDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}

	var err error

	if config.SchemaName == "" {
		if err = config.DB.QueryRowContext(ctx, "SELECT SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA') FROM DUAL").Scan(&config.SchemaName); err != nil {
			return nil, fmt.Errorf("oracle: read current schema: %w", err)
		}
		if err = identifier(config.SchemaName).validate("current schema"); err != nil {
			return nil, err
		}
	}
	fullTable := config.SchemaName + "." + config.TableName

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

	if err = store.requireContentCharacterSet(ctx); err != nil {
		return nil, err
	}
	if err = store.initialize(ctx, config.InitializeSchema); err != nil {
		return nil, fmt.Errorf("oracle: initialize store: %w", err)
	}
	if _, err := store.binding(ctx); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) initialize(ctx context.Context, initSchema bool) error {
	if !initSchema {
		return s.validateSchema(ctx)
	}
	if _, err := s.db.ExecContext(ctx, s.createTableStatement()); err != nil {
		return fmt.Errorf("create table %s: %w", s.fullTable, err)
	}
	return s.validateSchema(ctx)
}

func (s *Store) createTableStatement() string {
	return fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		%s RAW(%d) NOT NULL PRIMARY KEY,
		%s %s NOT NULL,
		%s BLOB NOT NULL,
		%s VECTOR(%d, FLOAT32) NOT NULL,
CONSTRAINT %s CHECK (1=1)
	)`, s.fullTable, s.idColumn, documentIDBytes, s.contentColumn, contentColumnType, s.metadataColumn, s.embeddingColumn, s.dimensions, s.space.StorageKey())
}

func (s *Store) validateSchema(ctx context.Context) (err error) {
	const (
		columnNotNullable       = "N"
		constraintStatusEnabled = "ENABLED"
		constraintValidated     = "VALIDATED"
	)
	var dataType, nullable string
	var length int
	const columns = "SELECT DATA_TYPE, DATA_LENGTH, NULLABLE FROM ALL_TAB_COLUMNS WHERE OWNER = UPPER(:1) AND TABLE_NAME = UPPER(:2) AND COLUMN_NAME = UPPER(:3)"
	if err = s.db.QueryRowContext(ctx, columns, s.schemaName, s.tableName, s.idColumn).Scan(&dataType, &length, &nullable); err != nil {
		return fmt.Errorf("read document ID schema: %w", err)
	}
	if dataType != "RAW" || length != documentIDBytes || nullable != columnNotNullable {
		return fmt.Errorf("document ID column must be RAW(%d) NOT NULL; rebuild the table", documentIDBytes)
	}
	if err = s.db.QueryRowContext(ctx, columns, s.schemaName, s.tableName, s.metadataColumn).Scan(&dataType, &length, &nullable); err != nil {
		return fmt.Errorf("read metadata schema: %w", err)
	}
	if dataType != "BLOB" || nullable != columnNotNullable {
		return errors.New("metadata column must be BLOB NOT NULL to retain encoded JSON; rebuild the table")
	}
	if err = s.db.QueryRowContext(ctx, columns, s.schemaName, s.tableName, s.contentColumn).Scan(&dataType, &length, &nullable); err != nil {
		return fmt.Errorf("read content schema: %w", err)
	}
	if dataType != contentColumnType || nullable != columnNotNullable {
		return fmt.Errorf("content column must be %s NOT NULL; rebuild the table", contentColumnType)
	}
	rows, err := s.db.QueryContext(ctx,
		"SELECT c.CONSTRAINT_TYPE, c.STATUS, c.VALIDATED, cc.COLUMN_NAME, cc.POSITION FROM ALL_CONSTRAINTS c JOIN ALL_CONS_COLUMNS cc ON cc.OWNER = c.OWNER AND cc.CONSTRAINT_NAME = c.CONSTRAINT_NAME WHERE c.OWNER = UPPER(:1) AND c.TABLE_NAME = UPPER(:2) AND c.CONSTRAINT_TYPE IN ('P', 'U')", s.schemaName, s.tableName)
	if err != nil {
		return fmt.Errorf("read document identity constraints: %w", err)
	}
	defer func(constraints *sql.Rows) {
		if closeErr := constraints.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}(rows)
	primary := false
	for rows.Next() {
		var kind, status, validated, column string
		var position int
		if err = rows.Scan(&kind, &status, &validated, &column, &position); err != nil {
			return fmt.Errorf("read document identity constraint: %w", err)
		}
		if !strings.EqualFold(column, s.idColumn) || position != 1 || status != constraintStatusEnabled || validated != constraintValidated {
			return errors.New("uniqueness constraints must identify a document solely by its full ID; rebuild the table")
		}
		primary = primary || kind == "P"
	}
	if err = rows.Err(); err != nil {
		return fmt.Errorf("read document identity constraints: %w", err)
	}
	if err = rows.Close(); err != nil {
		return fmt.Errorf("close document identity constraints: %w", err)
	}
	if !primary {
		return errors.New("document ID must be the sole primary key; rebuild the table")
	}
	rows, err = s.db.QueryContext(ctx,
		"SELECT cc.COLUMN_NAME, cc.COLUMN_POSITION FROM ALL_INDEXES i JOIN ALL_IND_COLUMNS cc ON cc.INDEX_OWNER = i.OWNER AND cc.INDEX_NAME = i.INDEX_NAME WHERE i.TABLE_OWNER = UPPER(:1) AND i.TABLE_NAME = UPPER(:2) AND i.UNIQUENESS = 'UNIQUE'", s.schemaName, s.tableName)
	if err != nil {
		return fmt.Errorf("read document identity indexes: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	for rows.Next() {
		var column string
		var position int
		if err = rows.Scan(&column, &position); err != nil {
			return fmt.Errorf("read document identity index: %w", err)
		}
		if !strings.EqualFold(column, s.idColumn) || position != 1 {
			return errors.New("unique indexes must identify a document solely by its full ID; rebuild the table")
		}
	}
	if err = rows.Err(); err != nil {
		return fmt.Errorf("read document identity indexes: %w", err)
	}
	return nil
}

func (s *Store) binding(ctx context.Context) (int, error) {
	if err := s.requireContentCharacterSet(ctx); err != nil {
		return 0, err
	}
	var shape string
	err := s.db.QueryRowContext(ctx, `SELECT VECTOR_INFO FROM ALL_TAB_COLS WHERE OWNER=UPPER(:1) AND TABLE_NAME=UPPER(:2) AND COLUMN_NAME=UPPER(:3)`, s.schemaName, s.tableName, s.embeddingColumn).Scan(&shape)
	if err != nil {
		return 0, err
	}
	var found int
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM ALL_CONSTRAINTS WHERE OWNER=UPPER(:1) AND TABLE_NAME=UPPER(:2) AND CONSTRAINT_TYPE='C' AND SUBSTR(CONSTRAINT_NAME,1,LENGTH(:4))=UPPER(:4) HAVING MIN(CONSTRAINT_NAME)=UPPER(:3) AND MAX(CONSTRAINT_NAME)=UPPER(:3)`, s.schemaName, s.tableName, s.space.StorageKey(), vectorstore.SpaceStorageKeyPrefix).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) || err == nil && found != 1 {
		return 0, vectorstore.ErrIncompatibleSpace
	}
	if err != nil {
		return 0, err
	}
	shape = strings.ToUpper(strings.ReplaceAll(shape, " ", ""))
	body, ok := strings.CutPrefix(shape, "VECTOR(")
	if !ok {
		return 0, errors.New("oracle: invalid VECTOR_INFO")
	}
	body, ok = strings.CutSuffix(body, ")")
	if !ok {
		return 0, errors.New("oracle: invalid VECTOR_INFO")
	}
	fields := strings.Split(body, ",")
	if len(fields) < 2 || len(fields) > 3 || fields[1] != "FLOAT32" || len(fields) == 3 && fields[2] != "DENSE" {
		return 0, errors.New("oracle: requires a fixed dense FLOAT32 vector")
	}
	dim, err := strconv.Atoi(fields[0])
	if err != nil || dim <= 0 {
		return 0, errors.New("oracle: invalid native vector width")
	}
	return dim, nil
}

func (s *Store) requireContentCharacterSet(ctx context.Context) error {
	var characterSet string
	if err := s.db.QueryRowContext(ctx, `SELECT VALUE FROM NLS_DATABASE_PARAMETERS WHERE PARAMETER = 'NLS_CHARACTERSET'`).Scan(&characterSet); err != nil {
		return fmt.Errorf("oracle: read content character set: %w", err)
	}
	// CLOB uses the database character set. The driver can replace unsupported
	// runes without an error, so the native binding must guarantee lossless UTF-8.
	if characterSet != contentCharacterSet {
		return fmt.Errorf("oracle: CLOB content requires database character set %s; found %q", contentCharacterSet, characterSet)
	}
	return nil
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
		vectorJSON, encodeErr := jsonv2.Marshal(corevector.Float32(r.Values))
		if encodeErr != nil {
			return encodeErr
		}
		content, encodeErr := oratypes.NewClob(nil, ctx, doc.Text, false)
		if encodeErr != nil {
			return encodeErr
		}
		facts, encodeErr := oratypes.NewBlob(nil, ctx, metaJSON)
		if encodeErr != nil {
			return encodeErr
		}
		args[i] = []any{[]byte(doc.ID), content, facts, string(vectorJSON)}
	}
	mergeSQL := fmt.Sprintf(
		`MERGE INTO %s tgt USING (SELECT :1 AS id, :2 AS content, :3 AS metadata, TO_VECTOR(:4, *, FLOAT32) AS embedding FROM dual) src `+
			`ON (tgt.%s = src.id) `+
			`WHEN MATCHED THEN UPDATE SET tgt.%s = src.content, tgt.%s = src.metadata, tgt.%s = src.embedding `+
			`WHEN NOT MATCHED THEN INSERT (tgt.%s, tgt.%s, tgt.%s, tgt.%s) VALUES (src.id, src.content, src.metadata, src.embedding)`,
		s.fullTable,
		s.idColumn,
		s.contentColumn, s.metadataColumn, s.embeddingColumn,
		s.idColumn, s.contentColumn, s.metadataColumn, s.embeddingColumn,
	)
	stmt, err := s.db.PrepareContext(ctx, mergeSQL)
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
				execErr = errors.New("oracle: missing native row acknowledgment")
			} else {
				var count int64
				count, execErr = result.RowsAffected()
				if execErr == nil && (count != 1) {
					execErr = fmt.Errorf("oracle: invalid upsert acknowledgment %d", count)
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
		return nil, fmt.Errorf("oracle: rank query: %w", err)
	}
	if err = request.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, fmt.Errorf("oracle: rank query: %w", err)
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
	var transaction *sql.Tx
	if request.Options.Filter != nil {
		transaction, err = s.db.BeginTx(ctx, nil)
		if err != nil {
			return nil, fmt.Errorf("oracle: begin filter transaction: %w", err)
		}
		defer rollbackTransaction(transaction, &err)
		if err = s.prepareFilter(ctx, transaction, request.Options.Filter, false, budget); err != nil {
			return nil, err
		}
	}

	vectorJSON, err := jsonv2.Marshal(corevector.Float32(request.Values))
	if err != nil {
		return nil, fmt.Errorf("oracle: marshal query vector: %w", err)
	}
	var ranked []rankedResult
	if request.Options.Filter == nil {
		ranked, err = s.searchRows(ctx, nil, request, string(vectorJSON), nil)
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
				candidates, queryErr := s.searchRows(ctx, transaction, request, string(vectorJSON), page.ids)
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
			return nil, fmt.Errorf("oracle: commit filter transaction: %w", err)
		}
	}
	return &vectorstore.SearchResponse{Results: results}, nil
}

type rankedResult struct {
	result   *vectorstore.SearchResult
	distance float64
}

func (s *Store) searchRows(ctx context.Context, transaction *sql.Tx, request *vectorstore.Query, vector string, ids [][]byte) (results []rankedResult, err error) {
	args := []any{vector}
	where := ""
	if len(ids) > 0 {
		placeholders := make([]string, len(ids))
		for index, id := range ids {
			args = append(args, id)
			placeholders[index] = ":" + strconv.Itoa(len(args))
		}
		where = " WHERE " + s.idColumn + " IN (" + strings.Join(placeholders, ", ") + ")"
	}
	args = append(args, request.Options.ResultLimit())
	statement := fmt.Sprintf(
		`SELECT %s, %s, %s, VECTOR_DISTANCE(%s, TO_VECTOR(:1, *, FLOAT32), %s) AS distance FROM %s%s ORDER BY distance ASC, %s ASC FETCH FIRST :%d ROWS ONLY`,
		s.idColumn, s.contentColumn, s.metadataColumn, s.embeddingColumn, s.distanceMetric, s.fullTable, where, s.idColumn, len(args))
	var rows *sql.Rows
	if transaction == nil {
		rows, err = s.db.QueryContext(ctx, statement, args...)
	} else {
		rows, err = transaction.QueryContext(ctx, statement, args...)
	}
	if err != nil {
		return nil, fmt.Errorf("oracle: query %s: %w", s.fullTable, err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	for rows.Next() {
		var id []byte
		var content sql.NullString
		var raw []byte
		var distance float64
		if err := rows.Scan(&id, &content, &raw, &distance); err != nil {
			return nil, fmt.Errorf("oracle: scan row: %w", err)
		}

		score := s.distanceMetric.score(distance)
		if err := score.Validate(); err != nil {
			return nil, fmt.Errorf("oracle: distance for %q: %w", id, err)
		}

		doc := &document.Document{ID: string(id), Text: content.String}
		if err := doc.UnmarshalStoredMetadata(raw); err != nil {
			return nil, fmt.Errorf("oracle: unmarshal metadata for %s: %w", id, err)
		}
		if err := vectorstore.ValidateDocument(doc); err != nil {
			return nil, fmt.Errorf("oracle: invalid native document %q: %w", id, err)
		}
		if score < request.Options.MinScore {
			continue
		}
		results = append(results, rankedResult{result: &vectorstore.SearchResult{Document: doc, Score: score}, distance: distance})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("oracle: read rows: %w", err)
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
		return fmt.Errorf("oracle.Store.DeleteMatching: %w", validateErr)
	}

	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("oracle: begin filter transaction: %w", err)
	}
	defer rollbackTransaction(transaction, &err)
	if err = s.prepareFilter(ctx, transaction, predicate, true, &budget); err != nil {
		return err
	}
	var lastID []byte
	for {
		page, pageErr := s.readFilterPage(ctx, transaction, predicate, lastID)
		if pageErr != nil {
			return pageErr
		}
		if len(page.ids) > 0 {
			placeholders := make([]string, len(page.ids))
			args := make([]any, len(page.ids))
			for index, id := range page.ids {
				placeholders[index] = ":" + strconv.Itoa(index+1)
				args[index] = id
			}
			result, execErr := transaction.ExecContext(ctx, "DELETE FROM "+s.fullTable+" WHERE "+s.idColumn+" IN ("+strings.Join(placeholders, ", ")+")", args...)
			if execErr != nil {
				return fmt.Errorf("oracle: delete matching documents: %w", execErr)
			}
			count, countErr := result.RowsAffected()
			if countErr != nil {
				return fmt.Errorf("oracle: read matching deletion count: %w", countErr)
			}
			if count != int64(len(page.ids)) {
				return fmt.Errorf("oracle: matching documents changed during deletion: deleted %d of %d snapshot documents", count, len(page.ids))
			}
		}
		if page.count < filterPageSize {
			break
		}
		lastID = page.lastID
	}
	if err = transaction.Commit(); err != nil {
		return fmt.Errorf("oracle: commit filter transaction: %w", err)
	}
	return nil
}

func (s *Store) DeleteIDs(ctx context.Context, ids []string) (err error) {
	if len(ids) == 0 {
		return nil
	}

	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = ":" + strconv.Itoa(i+1)
		args[i] = []byte(id)
	}

	stmt := fmt.Sprintf("DELETE FROM %s WHERE %s IN (%s)",
		s.fullTable, s.idColumn, strings.Join(placeholders, ", "))
	if _, err = s.db.ExecContext(ctx, stmt, args...); err != nil {
		return fmt.Errorf("oracle: delete by ids from %s: %w", s.fullTable, err)
	}
	return nil
}

const filterPageSize = 512

type filterPage struct {
	lastID []byte
	ids    [][]byte
	count  int
}

func (s *Store) readFilterPage(ctx context.Context, transaction *sql.Tx, predicate filter.Predicate, lastID []byte) (page filterPage, err error) {
	statement := "SELECT " + s.idColumn + ", " + s.metadataColumn + " FROM " + s.fullTable
	var args []any
	if lastID != nil {
		statement += " WHERE " + s.idColumn + " > :1"
		args = append(args, lastID)
	}
	args = append(args, filterPageSize)
	statement += " ORDER BY " + s.idColumn + " ASC FETCH FIRST :" + strconv.Itoa(len(args)) + " ROWS ONLY"
	rows, err := transaction.QueryContext(ctx, statement, args...)
	if err != nil {
		return page, fmt.Errorf("oracle: read filter metadata: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	for rows.Next() {
		var id, raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return page, fmt.Errorf("oracle: scan filter metadata: %w", err)
		}
		fields := new(document.Document)
		if err := fields.UnmarshalStoredMetadata(raw); err != nil {
			return page, fmt.Errorf("oracle: decode filter metadata: %w", err)
		}
		values, err := fields.Metadata.Values()
		if err != nil {
			return page, fmt.Errorf("oracle: decode filter values: %w", err)
		}
		match, err := filter.Match(predicate, values)
		if err != nil {
			return page, fmt.Errorf("oracle: evaluate filter: %w", err)
		}
		page.lastID = id
		page.count++
		if match {
			page.ids = append(page.ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		return page, fmt.Errorf("oracle: read filter metadata: %w", err)
	}
	return page, nil
}

// go-ora v3 cannot set isolation through TxOptions. Search needs a native
// snapshot. Deletion locks the table before evaluation because SERIALIZABLE
// can reject unchanged rows through Oracle's block-level conflict detection.
// NOWAIT leaves lock contention explicit without queuing a native operation
// that the driver's in-band cancellation may fail to interrupt.
func (s *Store) prepareFilter(ctx context.Context, transaction *sql.Tx, predicate filter.Predicate, forDeletion bool, budget *vectorstore.ScanBudget) error {
	isolation := "SET TRANSACTION ISOLATION LEVEL SERIALIZABLE"
	if forDeletion {
		isolation = "SET TRANSACTION ISOLATION LEVEL READ COMMITTED"
	}
	if _, err := transaction.ExecContext(ctx, isolation); err != nil {
		return fmt.Errorf("oracle: set filter transaction isolation: %w", err)
	}
	if forDeletion {
		if _, err := transaction.ExecContext(ctx, "LOCK TABLE "+s.fullTable+" IN EXCLUSIVE MODE NOWAIT"); err != nil {
			return fmt.Errorf("oracle: lock documents for filtered deletion: %w", err)
		}
	}
	if budget == nil {
		return vectorstore.ErrUnsupportedFilter
	}
	var allowed int
	statement := fmt.Sprintf("SELECT CASE WHEN COUNT(*)<=:1 AND 2*COALESCE(SUM(n),0)<=:2 THEN 1 ELSE 0 END FROM (SELECT UTL_RAW.LENGTH(%s)+COALESCE(DBMS_LOB.GETLENGTH(%s),0) n FROM %s FETCH FIRST :3 ROWS ONLY)", s.idColumn, s.metadataColumn, s.fullTable)
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
		*err = errors.Join(*err, fmt.Errorf("oracle: rollback filter transaction: %w", rollbackErr))
	}
}
