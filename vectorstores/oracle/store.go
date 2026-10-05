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

	"github.com/samber/lo"
	go_ora "github.com/sijms/go-ora/v2"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

const documentIDBytes = 2000

// Provider is the stable backend name for host-side attribution.
const Provider = "Oracle"

// Exported defaults keep constructor behavior visible and overridable.
const (
	DefaultTableName       = "VECTOR_STORE"
	DefaultIDColumn        = "ID"
	DefaultContentColumn   = "CONTENT"
	DefaultMetadataColumn  = "METADATA"
	DefaultEmbeddingColumn = "EMBEDDING"
	DefaultDistanceMetric  = DistanceCosine
)

// DistanceMetric selects the VECTOR_DISTANCE function variant. The
// constants mirror Oracle's accepted values exactly so they can flow
// straight into the SQL.
type DistanceMetric string

const (
	// DistanceCosine — cosine distance.
	DistanceCosine DistanceMetric = "COSINE"

	// DistanceEuclidean — Euclidean (L2) distance.
	DistanceEuclidean DistanceMetric = "EUCLIDEAN"

	// DistanceDot selects Oracle's negative inner-product distance.
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

// StoreConfig contains configuration options for the Oracle 23ai
// vector store.
type StoreConfig struct {
	// DB is the database handle. Required. Use a *sql.DB built from
	// github.com/sijms/go-ora/v2 pointed at an Oracle 23ai
	// instance.
	DB *sql.DB

	// SchemaName is the optional schema prefix (Oracle username).
	// When empty the connection's current schema is resolved at construction.
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

	// Dimensions sets the VECTOR column width when InitializeSchema is true.
	// Runtime vector construction derives its width from the input; the native
	// column owns the stored dimension constraint.
	Dimensions int

	// DistanceMetric selects the distance function. Optional:
	// defaults to [DistanceCosine].
	DistanceMetric DistanceMetric

	// InitializeSchema creates the current table when absent. Construction
	// always verifies exact document identity and lossless metadata storage.
	InitializeSchema bool
}

func (s StoreConfig) Validate() error {
	s.applyDefaults()
	if s.DB == nil {
		return errors.New("oracle: DB is required")
	}
	if lo.IsNil(s.EmbeddingModel) {
		return errors.New("oracle: EmbeddingModel is required")
	}
	if lo.IsNil(s.DocumentBatcher) {
		return errors.New("oracle: DocumentBatcher is required")
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

// Store implements vector-store capabilities with Oracle AI Vector Search.
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

// NewStore creates the table when requested and verifies the current storage
// contract. The host owns the database handle. An omitted schema is bound to
// the connection's current schema at construction.
func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	config.applyDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}

	embeddingClient, err := embeddingclient.New(config.EmbeddingModel)
	if err != nil {
		return nil, fmt.Errorf("oracle: create embedding client: %w", err)
	}

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
		embeddingClient: embeddingClient,
		documentBatcher: config.DocumentBatcher,
		dimensions:      config.Dimensions,
		distanceMetric:  config.DistanceMetric,
	}

	if err = store.initialize(ctx, config.InitializeSchema); err != nil {
		return nil, fmt.Errorf("oracle: initialize store: %w", err)
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
		%s CLOB NOT NULL,
		%s BLOB NOT NULL,
		%s VECTOR(%d, FLOAT32) NOT NULL
	)`, s.fullTable, s.idColumn, documentIDBytes, s.contentColumn, s.metadataColumn, s.embeddingColumn, s.dimensions)
}

func (s *Store) validateSchema(ctx context.Context) (err error) {
	var dataType, nullable string
	var length int
	const columns = "SELECT DATA_TYPE, DATA_LENGTH, NULLABLE FROM ALL_TAB_COLUMNS WHERE OWNER = UPPER(:1) AND TABLE_NAME = UPPER(:2) AND COLUMN_NAME = UPPER(:3)"
	if err = s.db.QueryRowContext(ctx, columns, s.schemaName, s.tableName, s.idColumn).Scan(&dataType, &length, &nullable); err != nil {
		return fmt.Errorf("read document ID schema: %w", err)
	}
	if dataType != "RAW" || length != documentIDBytes || nullable != "N" {
		return fmt.Errorf("document ID column must be RAW(%d) NOT NULL; rebuild the table", documentIDBytes)
	}
	if err = s.db.QueryRowContext(ctx, columns, s.schemaName, s.tableName, s.metadataColumn).Scan(&dataType, &length, &nullable); err != nil {
		return fmt.Errorf("read metadata schema: %w", err)
	}
	if dataType != "BLOB" || nullable != "N" {
		return errors.New("metadata column must be BLOB NOT NULL to retain encoded JSON; rebuild the table")
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
		if !strings.EqualFold(column, s.idColumn) || position != 1 || status != "ENABLED" || validated != "VALIDATED" {
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

// Index embeds documents and upserts them via MERGE.
func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) (err error) {
	if validateErr := request.Validate(); validateErr != nil {
		return fmt.Errorf("oracle.Store.Index: %w", validateErr)
	}
	for index, doc := range request.Documents {
		if len(doc.ID) > documentIDBytes {
			return fmt.Errorf("oracle.Store.Index: %w: documents[%d] ID exceeds %d bytes", vectorstore.ErrInvalidDocument, index, documentIDBytes)
		}
		if doc.Media != nil {
			return fmt.Errorf("oracle.Store.Index: %w: documents[%d] contains unsupported media", vectorstore.ErrInvalidDocument, index)
		}
	}

	var batches []*vectorstore.IndexRequest
	batches, err = request.Batch(ctx, s.documentBatcher)
	if err != nil {
		return fmt.Errorf("oracle: batch documents: %w", err)
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

	for _, batch := range batches {
		docs := batch.Documents
		texts, err := batch.Texts()
		if err != nil {
			return fmt.Errorf("vectorstore: project document text: %w", err)
		}
		vectors, err := s.embeddingClient.EmbedTexts(ctx, texts)
		if err != nil {
			return fmt.Errorf("oracle: embed documents: %w", err)
		}

		stmt, err := s.db.PrepareContext(ctx, mergeSQL)
		if err != nil {
			return fmt.Errorf("oracle: prepare merge: %w", err)
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
					return fmt.Errorf("oracle: marshal vector for %s: %w", id, err)
				}
				if _, err := stmt.ExecContext(ctx, []byte(id), go_ora.Clob{String: doc.Text, Valid: true}, go_ora.Blob{Data: metaJSON}, string(vectorJSON)); err != nil {
					return fmt.Errorf("merge %s: %w", id, err)
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

// Search evaluates filters through Core and ranks the same snapshot with
// native exact distance and ID ordering.
func (s *Store) Search(ctx context.Context, request *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	if err = request.Validate(); err != nil {
		return nil, fmt.Errorf("oracle.Store.Search: %w", err)
	}
	if err = request.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, fmt.Errorf("oracle.Store.Search: %w", err)
	}

	defer func() {
		if err == nil {
			err = response.ValidateFor(request)
		}
		if err != nil {
			response = nil
		}
	}()
	transaction, err := s.prepareFilter(ctx, request.Options.Filter, false)
	defer finishTransaction(transaction, &err)
	if err != nil {
		return nil, err
	}
	vector, err := s.embeddingClient.EmbedText(ctx, request.Query)
	if err != nil {
		return nil, fmt.Errorf("oracle: embed query: %w", err)
	}
	vectorJSON, err := jsonv2.Marshal(embedding.Float32Vector(vector))
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
	return &vectorstore.SearchResponse{Results: results}, nil
}

type rankedResult struct {
	result   *vectorstore.SearchResult
	distance float64
}

func (s *Store) searchRows(ctx context.Context, transaction *sql.Tx, request *vectorstore.SearchRequest, vector string, ids [][]byte) (results []rankedResult, err error) {
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
		if err := jsonv2.Unmarshal(raw, &doc.Metadata); err != nil {
			return nil, fmt.Errorf("oracle: unmarshal metadata for %s: %w", id, err)
		}
		if err := (&vectorstore.IndexRequest{Documents: []*document.Document{doc}}).Validate(); err != nil {
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

func (s *Store) DeleteWhere(ctx context.Context, predicate filter.Predicate) (err error) {
	if predicate == nil {
		return vectorstore.ErrMissingFilter
	}
	if validateErr := predicate.Validate(); validateErr != nil {
		return fmt.Errorf("oracle.Store.DeleteWhere: %w", validateErr)
	}

	transaction, err := s.prepareFilter(ctx, predicate, true)
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
			return nil
		}
		lastID = page.lastID
	}
}

// DeleteIDs removes rows by primary key — `DELETE ... WHERE <id> IN
// (:1, :2, …)` with one positional bind per id. An empty slice is a
// no-op; unknown ids are silently ignored (idempotent). Implements
// [vectorstore.IDDeleter].
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
		var facts metadata.Map
		if err := jsonv2.Unmarshal(raw, &facts); err != nil {
			return page, fmt.Errorf("oracle: decode filter metadata: %w", err)
		}
		values, err := facts.Values()
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

// go-ora v2 cannot set isolation through TxOptions. Search needs a native
// snapshot. Deletion locks the table before evaluation because SERIALIZABLE
// can reject unchanged rows through Oracle's block-level conflict detection.
// NOWAIT leaves lock contention explicit without queuing a native operation
// that the driver's in-band cancellation may fail to interrupt.
func (s *Store) prepareFilter(ctx context.Context, predicate filter.Predicate, forDeletion bool) (*sql.Tx, error) {
	if predicate == nil {
		return nil, nil
	}
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("oracle: begin filter transaction: %w", err)
	}
	isolation := "SET TRANSACTION ISOLATION LEVEL SERIALIZABLE"
	if forDeletion {
		isolation = "SET TRANSACTION ISOLATION LEVEL READ COMMITTED"
	}
	if _, err := transaction.ExecContext(ctx, isolation); err != nil {
		return transaction, fmt.Errorf("oracle: set filter transaction isolation: %w", err)
	}
	if forDeletion {
		if _, err := transaction.ExecContext(ctx, "LOCK TABLE "+s.fullTable+" IN EXCLUSIVE MODE NOWAIT"); err != nil {
			return transaction, fmt.Errorf("oracle: lock documents for filtered deletion: %w", err)
		}
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
			*err = fmt.Errorf("oracle: commit filter transaction: %w", commitErr)
		}
		return
	}
	if rollbackErr := transaction.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
		*err = errors.Join(*err, fmt.Errorf("oracle: rollback filter transaction: %w", rollbackErr))
	}
}
