// Package pgstore owns the shared pgwire vector-store execution boundary.
package pgstore

import (
	"context"

	"errors"
	"fmt"
	"strings"

	"github.com/Tangerg/scope/vectorstores/postgres"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pgvec "github.com/pgvector/pgvector-go"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

type Config struct {
	Provider       string
	Pool           *pgxpool.Pool
	SchemaName     string
	TableName      string
	MetadataColumn string
	Space          vectorstore.SpaceID
	DistanceMetric postgres.DistanceMetric
}

type Store struct {
	provider       string
	pool           *pgxpool.Pool
	fullTable      string
	metadataColumn string
	metadataName   string
	space          vectorstore.SpaceID
	distanceMetric postgres.DistanceMetric
}

func New(ctx context.Context, config Config) (Store, error) {
	if config.Pool == nil || !config.DistanceMetric.Valid() {
		return Store{}, errors.New("pgstore: incomplete construction dependencies")
	}
	if err := config.Space.Validate(); err != nil {
		return Store{}, err
	}
	store := Store{provider: config.Provider, pool: config.Pool, fullTable: pgx.Identifier{config.SchemaName, config.TableName}.Sanitize(), metadataColumn: pgx.Identifier{config.MetadataColumn}.Sanitize(), metadataName: config.MetadataColumn, space: config.Space, distanceMetric: config.DistanceMetric}
	if _, err := store.readSchema(ctx, config.Pool); err != nil {
		return Store{}, err
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
	defer func() {
		if err == nil {
			if commitErr := transaction.Commit(ctx); errors.Is(commitErr, pgx.ErrTxCommitRollback) {
				err = commitErr
			} else if commitErr != nil {
				ids := make([]string, len(batch.Records))
				for i, r := range batch.Records {
					ids[i] = r.Document.ID
				}
				err = &vectorstore.WriteError{Unconfirmed: ids, Err: commitErr}
			}
		}
		if rollbackErr := transaction.Rollback(ctx); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			err = errors.Join(err, rollbackErr)
		}
	}()
	width, err := s.readSchema(ctx, transaction)
	if err != nil {
		return err
	}
	prepared := &pgx.Batch{}
	statement := fmt.Sprintf("INSERT INTO %s (id,content,%s,embedding) VALUES ($1,$2,$3,$4) ON CONFLICT(id) DO UPDATE SET content=EXCLUDED.content,%s=EXCLUDED.%s,embedding=EXCLUDED.embedding", s.fullTable, s.metadataColumn, s.metadataColumn, s.metadataColumn)
	for _, record := range batch.Records {
		doc := record.Document
		facts, err := doc.MarshalStoredMetadata()
		if err != nil {
			return err
		}
		coords, err := s.distanceMetric.Vector(record.Values, width)
		if err != nil {
			return err
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
			return errors.Join(errors.New("pgstore: upsert did not acknowledge one row"), results.Close())
		}
	}
	return results.Close()
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
			err = fmt.Errorf("%s.Store.Query: %w", s.provider, err)
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
		s.finishTransaction(ctx, transaction, &err)
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
	return response, nil
}

func (s *Store) DeleteIDs(ctx context.Context, ids []string) (err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("%s.Store.DeleteIDs: %w", s.provider, err)
		}
	}()
	if len(ids) == 0 {
		return nil
	}
	nativeIDs := make([][]byte, len(ids))
	for index, id := range ids {
		nativeIDs[index] = []byte(id)
	}
	transaction, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer s.finishTransaction(ctx, transaction, &err)
	if _, err = s.readSchema(ctx, transaction); err != nil {
		return err
	}
	_, err = transaction.Exec(ctx, "DELETE FROM "+s.fullTable+" WHERE id=ANY($1)", nativeIDs)
	return err
}

func (s *Store) readSchema(ctx context.Context, query rowQuerier) (int, error) {
	rows, err := query.Query(ctx, `SELECT a.attname,t.typname,a.atttypmod,a.attnotnull,a.atthasdef,a.attnum FROM pg_attribute a JOIN pg_type t ON t.oid=a.atttypid WHERE a.attrelid=to_regclass($1) AND a.attnum>0 AND NOT a.attisdropped ORDER BY a.attnum`, s.fullTable)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	width, idNumber, count := 0, 0, 0
	for rows.Next() {
		var name, kind string
		var modifier, number int
		var required, defaults bool
		if err = rows.Scan(&name, &kind, &modifier, &required, &defaults, &number); err != nil {
			return 0, err
		}
		if !required || defaults {
			return 0, errors.New("pgstore: current columns must be non-null without defaults")
		}
		count++
		switch name {
		case "id":
			if kind != "bytea" {
				return 0, errors.New("pgstore: id must be binary")
			}
			idNumber = number
		case "content":
			if kind != "text" {
				return 0, errors.New("pgstore: content must be text")
			}
		case s.metadataName:
			if kind != "bytea" {
				return 0, errors.New("pgstore: metadata must contain Core JSON bytes")
			}
		case "embedding":
			if kind != "vector" || modifier <= 0 {
				return 0, errors.New("pgstore: embedding must have a fixed native vector width")
			}
			width = modifier
		default:
			return 0, fmt.Errorf("pgstore: unexpected current column %q", name)
		}
	}
	if err = rows.Err(); err != nil {
		return 0, err
	}
	rows.Close()
	if count != 4 || width == 0 || idNumber == 0 {
		return 0, errors.New("pgstore: current four-column schema is required")
	}
	primary, err := query.Query(ctx, `SELECT conkey FROM pg_constraint WHERE conrelid=to_regclass($1) AND contype='p'`, s.fullTable)
	if err != nil {
		return 0, err
	}
	defer primary.Close()
	if !primary.Next() {
		if err = primary.Err(); err != nil {
			return 0, err
		}
		return 0, errors.New("pgstore: native primary key is required")
	}
	var keys []int16
	if err = primary.Scan(&keys); err != nil {
		return 0, err
	}
	if len(keys) != 1 || int(keys[0]) != idNumber || primary.Next() {
		return 0, errors.New("pgstore: native primary key must be only the binary ID")
	}
	if err = primary.Err(); err != nil {
		return 0, err
	}
	primary.Close()
	// Read every space marker: multiple bindings cannot authorize mixed spaces.
	binding, err := query.Query(ctx, `SELECT conname FROM pg_constraint WHERE conrelid=to_regclass($1) AND contype='c' AND left(conname,3)='sv_'`, s.fullTable)
	if err != nil {
		return 0, err
	}
	defer binding.Close()
	if !binding.Next() {
		if err := binding.Err(); err != nil {
			return 0, err
		}
		return 0, fmt.Errorf("%w: table has no space constraint", vectorstore.ErrIncompatibleSpace)
	}
	var name string
	if err := binding.Scan(&name); err != nil {
		return 0, err
	}
	if name != s.space.StorageKey() || binding.Next() {
		return 0, fmt.Errorf("%w: wrong or ambiguous collection binding", vectorstore.ErrIncompatibleSpace)
	}
	if err := binding.Err(); err != nil {
		return 0, err
	}
	return width, nil
}

// Scan never returns partial membership. Preflight is performed in the same
// snapshot as the read, before the client receives any document payload.
func (s *Store) Scan(ctx context.Context, config postgres.ScanConfig) (result []*document.Document, err error) {
	if err = config.Validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, config.Budget.Timeout)
	defer cancel()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() {
		s.finishTransaction(ctx, tx, &err)
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
	if err = tx.QueryRow(ctx, stmt, config.Budget.MaxRecords+1, config.Budget.MaxRecords, config.Budget.MaxBytes, width).Scan(&fits); err != nil {
		return nil, err
	}
	if !fits {
		return nil, vectorstore.ErrScanBudget
	}
	docs, err := s.readSource(ctx, tx, width)
	if err != nil {
		return nil, err
	}
	return selectRecords(ctx, docs, config.Filter)
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

func (s *Store) finishTransaction(ctx context.Context, transaction pgx.Tx, operationErr *error) {
	if *operationErr == nil {
		*operationErr = transaction.Commit(ctx)
	}
	if err := transaction.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
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
		return nil, errors.New("pgstore: native content contains NUL")
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
			values, err := doc.Metadata.Values()
			if err != nil {
				return nil, err
			}
			matches, err := filter.Match(predicate, values)
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
