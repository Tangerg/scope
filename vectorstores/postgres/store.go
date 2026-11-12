package postgres

import (
	"context"

	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pgvec "github.com/pgvector/pgvector-go"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

const transactionCleanupTimeout = 5 * time.Second

// StoreConfig binds an existing table in the strict current schema.
type StoreConfig struct {
	Pool           *pgxpool.Pool
	SchemaName     string
	TableName      string
	MetadataColumn string
	Space          vectorstore.SpaceID
	DistanceMetric DistanceMetric
	// ExpectedDimensions, when positive, is the width the table must already
	// declare; the native column owns it and zero accepts whatever it declares.
	ExpectedDimensions int
}

// Store executes the Core vector store contracts against one pgwire table; the
// pgvector and cockroachdb constructors provision its engine-specific schema.
type Store struct {
	pool           *pgxpool.Pool
	fullTable      string
	metadataColumn string
	metadataName   string
	space          vectorstore.SpaceID
	distanceMetric DistanceMetric
}

func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	if config.Pool == nil || !config.DistanceMetric.Valid() {
		return nil, errors.New("postgres: incomplete construction dependencies")
	}
	if err := config.Space.Validate(); err != nil {
		return nil, err
	}
	store := &Store{pool: config.Pool, fullTable: pgx.Identifier{config.SchemaName, config.TableName}.Sanitize(), metadataColumn: pgx.Identifier{config.MetadataColumn}.Sanitize(), metadataName: config.MetadataColumn, space: config.Space, distanceMetric: config.DistanceMetric}
	width, err := store.readSchema(ctx, config.Pool)
	if err != nil {
		return nil, err
	}
	if config.ExpectedDimensions > 0 && width != config.ExpectedDimensions {
		return nil, fmt.Errorf("%w: table declares %d dimensions, not %d", vectorstore.ErrIncompatibleSpace, width, config.ExpectedDimensions)
	}
	return store, nil
}

func validateText(doc *document.Document) error {
	if doc.Media != nil || strings.ContainsRune(doc.Text, 0) {
		return fmt.Errorf("%w: PostgreSQL requires text without NUL and does not support media", vectorstore.ErrInvalidDocument)
	}
	return nil
}

func (s *Store) CheckDocuments(ctx context.Context, space vectorstore.SpaceID, docs []*document.Document) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := vectorstore.ValidateDocuments(docs); err != nil {
		return err
	}
	for _, doc := range docs {
		if err := validateText(doc); err != nil {
			return err
		}
	}
	if err := space.Require(s.space); err != nil {
		return err
	}
	_, err := s.readSchema(ctx, s.pool)
	return err
}

func (s *Store) Upsert(ctx context.Context, batch *vectorstore.Batch) (err error) {
	if err = batch.ValidateFor(s.space); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	for _, r := range batch.Records {
		if err = validateText(r.Document); err != nil {
			return err
		}
	}
	transaction, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer s.rollbackTransaction(ctx, transaction, &err)
	width, err := s.readSchema(ctx, transaction)
	if err != nil {
		return err
	}
	prepared := &pgx.Batch{}
	statement := fmt.Sprintf("INSERT INTO %s (id,content,%s,embedding) VALUES ($1,$2,$3,$4) ON CONFLICT(id) DO UPDATE SET content=EXCLUDED.content,%s=EXCLUDED.%s,embedding=EXCLUDED.embedding", s.fullTable, s.metadataColumn, s.metadataColumn, s.metadataColumn)
	for _, record := range batch.Records {
		doc := record.Document
		facts, metadataErr := doc.MarshalStoredMetadata()
		if metadataErr != nil {
			return metadataErr
		}
		coords, vectorErr := s.distanceMetric.Vector(record.Values, width)
		if vectorErr != nil {
			return vectorErr
		}
		prepared.Queue(statement, []byte(doc.ID), doc.Text, facts, coords)
	}
	results := transaction.SendBatch(ctx, prepared)
	for range prepared.Len() {
		tag, execErr := results.Exec()
		if execErr != nil {
			return errors.Join(execErr, results.Close())
		}
		if tag.RowsAffected() != 1 {
			return errors.Join(errors.New("postgres: upsert did not acknowledge one row"), results.Close())
		}
	}
	if err = results.Close(); err != nil {
		return err
	}
	if err = transaction.Commit(ctx); err == nil || errors.Is(err, pgx.ErrTxCommitRollback) {
		return err
	}
	ids := make([]string, len(batch.Records))
	for index, record := range batch.Records {
		ids[index] = record.Document.ID
	}
	return vectorstore.NewWriteError(err, vectorstore.WriteOutcome{Unconfirmed: ids})
}

// Metadata is stored as opaque Core JSON bytes. Without an equivalent native
// predicate, rejecting the request preserves tenant isolation and global top K.
func (s *Store) CheckQuery(ctx context.Context, space vectorstore.SpaceID, options vectorstore.SearchOptions) error {
	if err := s.validateQuery(ctx, space, options); err != nil {
		return err
	}
	_, err := s.readSchema(ctx, s.pool)
	return err
}

func (s *Store) validateQuery(ctx context.Context, space vectorstore.SpaceID, options vectorstore.SearchOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := space.Require(s.space); err != nil {
		return err
	}
	if err := options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return err
	}
	if options.Filter != nil {
		return vectorstore.ErrUnsupportedFilter
	}
	return nil
}

func (s *Store) Query(ctx context.Context, request *vectorstore.Query) (response *vectorstore.SearchResponse, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = fmt.Errorf("postgres.Store.Query: %w", err)
		}
	}()
	if err = request.ValidateFor(s.space); err != nil {
		return nil, err
	}
	if err = s.validateQuery(ctx, request.Space, request.Options); err != nil {
		return nil, err
	}
	transaction, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() {
		s.rollbackTransaction(ctx, transaction, &err)
		if err != nil {
			response = nil
		}
	}()
	width, err := s.readSchema(ctx, transaction)
	if err != nil {
		return nil, err
	}
	response = &vectorstore.SearchResponse{Results: []*vectorstore.SearchResult{}}
	queryVector, err := s.distanceMetric.Vector(request.Values, width)
	if err != nil {
		return nil, err
	}
	statement := fmt.Sprintf("SELECT id,content,%s,embedding,embedding %s $1 AS distance FROM %s ORDER BY distance,id LIMIT $2", s.metadataColumn, s.distanceMetric.Operator(), s.fullTable)
	rows, err := transaction.Query(ctx, statement, queryVector, request.Options.ResultLimit())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id []byte
		var text string
		var facts []byte
		var vector pgvec.Vector
		var distance float64
		if err = rows.Scan(&id, &text, &facts, &vector, &distance); err != nil {
			return nil, err
		}
		doc, decodeErr := decodeDocument(id, text, facts)
		if decodeErr != nil {
			return nil, decodeErr
		}
		if err = s.validateNativeVector(vector, width); err != nil {
			return nil, err
		}
		score, scoreErr := s.distanceMetric.Score(distance)
		if scoreErr != nil {
			return nil, scoreErr
		}
		if score >= request.Options.MinScore {
			response.Results = append(response.Results, &vectorstore.SearchResult{Document: doc, Score: score})
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = request.ValidateResponse(response); err != nil {
		return nil, err
	}
	if err = transaction.Commit(ctx); err != nil {
		return nil, err
	}
	return response, nil
}

func (s *Store) DeleteIDs(ctx context.Context, ids []string) (err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("postgres.Store.DeleteIDs: %w", err)
		}
	}()
	ids, err = vectorstore.DistinctIDs(ids)
	if err != nil || len(ids) == 0 {
		return err
	}
	nativeIDs := make([][]byte, len(ids))
	for index, id := range ids {
		nativeIDs[index] = []byte(id)
	}
	transaction, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer s.rollbackTransaction(ctx, transaction, &err)
	if _, err = s.readSchema(ctx, transaction); err != nil {
		return err
	}
	if _, err = transaction.Exec(ctx, "DELETE FROM "+s.fullTable+" WHERE id=ANY($1)", nativeIDs); err != nil {
		return err
	}
	return transaction.Commit(ctx)
}

// readSchema admits the current four-column table, its binary primary key and
// its single space binding, returning the native vector width.
func (s *Store) readSchema(ctx context.Context, query rowQuerier) (int, error) {
	width, idNumber, err := s.readColumns(ctx, query)
	if err != nil {
		return 0, err
	}
	if err := s.requirePrimaryKey(ctx, query, idNumber); err != nil {
		return 0, err
	}
	if err := s.requireSpaceBinding(ctx, query); err != nil {
		return 0, err
	}
	return width, nil
}

func (s *Store) readColumns(ctx context.Context, query rowQuerier) (width, idNumber int, err error) {
	rows, err := query.Query(ctx, `SELECT a.attname,t.typname,a.atttypmod,a.attnotnull,a.atthasdef,a.attnum FROM pg_attribute a JOIN pg_type t ON t.oid=a.atttypid WHERE a.attrelid=to_regclass($1) AND a.attnum>0 AND NOT a.attisdropped ORDER BY a.attnum`, s.fullTable)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var name, kind string
		var modifier, number int
		var required, defaults bool
		if err = rows.Scan(&name, &kind, &modifier, &required, &defaults, &number); err != nil {
			return 0, 0, err
		}
		if !required || defaults {
			return 0, 0, errors.New("postgres: current columns must be non-null without defaults")
		}
		count++
		switch name {
		case "id":
			if kind != "bytea" {
				return 0, 0, errors.New("postgres: id must be binary")
			}
			idNumber = number
		case "content":
			if kind != "text" {
				return 0, 0, errors.New("postgres: content must be text")
			}
		case s.metadataName:
			if kind != "bytea" {
				return 0, 0, errors.New("postgres: metadata must contain Core JSON bytes")
			}
		case "embedding":
			if kind != "vector" || modifier <= 0 {
				return 0, 0, errors.New("postgres: embedding must have a fixed native vector width")
			}
			width = modifier
		default:
			return 0, 0, fmt.Errorf("postgres: unexpected current column %q", name)
		}
	}
	if err = rows.Err(); err != nil {
		return 0, 0, err
	}
	if count != 4 || width == 0 || idNumber == 0 {
		return 0, 0, errors.New("postgres: current four-column schema is required")
	}
	return width, idNumber, nil
}

func (s *Store) requirePrimaryKey(ctx context.Context, query rowQuerier, idNumber int) error {
	primary, err := query.Query(ctx, `SELECT conkey FROM pg_constraint WHERE conrelid=to_regclass($1) AND contype='p'`, s.fullTable)
	if err != nil {
		return err
	}
	defer primary.Close()
	if !primary.Next() {
		if err = primary.Err(); err != nil {
			return err
		}
		return errors.New("postgres: native primary key is required")
	}
	var keys []int16
	if err = primary.Scan(&keys); err != nil {
		return err
	}
	if len(keys) != 1 || int(keys[0]) != idNumber || primary.Next() {
		return errors.New("postgres: native primary key must be only the binary ID")
	}
	return primary.Err()
}

// requireSpaceBinding reads every space marker: multiple bindings cannot
// authorize mixed spaces.
func (s *Store) requireSpaceBinding(ctx context.Context, query rowQuerier) error {
	binding, err := query.Query(ctx, `SELECT conname FROM pg_constraint WHERE conrelid=to_regclass($1) AND contype='c' AND left(conname,length($2))=$2`, s.fullTable, vectorstore.SpaceStorageKeyPrefix)
	if err != nil {
		return err
	}
	defer binding.Close()
	if !binding.Next() {
		if err := binding.Err(); err != nil {
			return err
		}
		return fmt.Errorf("%w: table has no space constraint", vectorstore.ErrIncompatibleSpace)
	}
	var name string
	if err := binding.Scan(&name); err != nil {
		return err
	}
	if name != s.space.StorageKey() || binding.Next() {
		return fmt.Errorf("%w: wrong or ambiguous collection binding", vectorstore.ErrIncompatibleSpace)
	}
	return binding.Err()
}

// Scan never returns partial membership. Preflight is performed in the same
// snapshot as the read, before the client receives any document payload.
func (s *Store) Scan(ctx context.Context, request vectorstore.ScanRequest) (result []*document.Document, err error) {
	if err = request.Validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, request.Budget.Timeout)
	defer cancel()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() {
		s.rollbackTransaction(ctx, tx, &err)
		if err != nil {
			result = nil
		}
	}()
	width, err := s.readSchema(ctx, tx)
	if err != nil {
		return nil, err
	}
	var fits bool
	stmt := fmt.Sprintf("SELECT count(*) <= $2 AND COALESCE(sum(n),0) <= $3 FROM (SELECT octet_length(id)::bigint + octet_length(content) + octet_length(%s) + 4::bigint * $4 AS n FROM %s LIMIT $1) AS bounded", s.metadataColumn, s.fullTable)
	if err = tx.QueryRow(ctx, stmt, request.Budget.MaxRecords+1, request.Budget.MaxRecords, request.Budget.MaxBytes, width).Scan(&fits); err != nil {
		return nil, err
	}
	if !fits {
		return nil, vectorstore.ErrScanBudget
	}
	docs, err := s.readSource(ctx, tx, width)
	if err != nil {
		return nil, err
	}
	result, err = selectRecords(ctx, docs, request.Filter)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Store) readSource(ctx context.Context, transaction pgx.Tx, width int) ([]*document.Document, error) {
	statement := fmt.Sprintf("SELECT id,content,%s,embedding FROM %s ORDER BY id", s.metadataColumn, s.fullTable)

	rows, err := transaction.Query(ctx, statement)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	docs := make([]*document.Document, 0)
	for rows.Next() {
		var id []byte
		var text string
		var facts []byte
		var vector pgvec.Vector
		if err = rows.Scan(&id, &text, &facts, &vector); err != nil {
			return nil, err
		}
		doc, err := decodeDocument(id, text, facts)
		if err != nil {
			return nil, err
		}
		if err = s.validateNativeVector(vector, width); err != nil {
			return nil, err
		}
		docs = append(docs, doc)
	}
	return docs, rows.Err()
}

func (s *Store) validateNativeVector(vector pgvec.Vector, width int) error {
	values := make([]float64, len(vector.Slice()))
	for index, value := range vector.Slice() {
		values[index] = float64(value)
	}
	_, err := s.distanceMetric.Vector(values, width)
	return err
}

func (s *Store) rollbackTransaction(ctx context.Context, transaction pgx.Tx, operationErr *error) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), transactionCleanupTimeout)
	defer cancel()
	if err := transaction.Rollback(cleanup); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		*operationErr = errors.Join(*operationErr, err)
	}
}

type rowQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func decodeDocument(id []byte, text string, facts []byte) (*document.Document, error) {
	doc := new(document.Document)
	if err := doc.UnmarshalStoredMetadata(facts); err != nil {
		return nil, err
	}
	doc.ID, doc.Text = string(id), text
	if err := vectorstore.ValidateDocument(doc); err != nil {
		return nil, err
	}
	if strings.ContainsRune(text, 0) {
		return nil, errors.New("postgres: native content contains NUL")
	}
	return doc, nil
}

func selectRecords(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]*document.Document, error) {
	ids := make([]*document.Document, 0, len(docs))
	for _, doc := range docs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if predicate != nil {
			matches, err := filter.Match(predicate, doc.Metadata)
			if err != nil {
				return nil, err
			}
			if !matches {
				continue
			}
		}
		ids = append(ids, doc)
	}
	return ids, nil
}
