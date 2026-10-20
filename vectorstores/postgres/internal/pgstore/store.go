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
	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

type Config struct {
	Provider        string
	Pool            *pgxpool.Pool
	SchemaName      string
	TableName       string
	MetadataColumn  string
	EmbeddingModel  embedding.Model
	DocumentBatcher vectorstore.Batcher
	DistanceMetric  postgres.DistanceMetric
}

type Store struct {
	provider        string
	pool            *pgxpool.Pool
	fullTable       string
	metadataColumn  string
	metadataName    string
	embeddingClient embeddingclient.Client
	documentBatcher vectorstore.Batcher
	distanceMetric  postgres.DistanceMetric
}

func New(ctx context.Context, config Config) (Store, error) {
	if config.Pool == nil || lo.IsNil(config.DocumentBatcher) || !config.DistanceMetric.Valid() {
		return Store{}, errors.New("pgstore: incomplete construction dependencies")
	}
	client, err := embeddingclient.New(config.EmbeddingModel)
	if err != nil {
		return Store{}, err
	}
	store := Store{provider: config.Provider, pool: config.Pool, fullTable: pgx.Identifier{config.SchemaName, config.TableName}.Sanitize(), metadataColumn: pgx.Identifier{config.MetadataColumn}.Sanitize(), metadataName: config.MetadataColumn, embeddingClient: client, documentBatcher: config.DocumentBatcher, distanceMetric: config.DistanceMetric}
	if _, err := store.readSchema(ctx, config.Pool); err != nil {
		return Store{}, err
	}
	return store, nil
}

func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) (err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("%s.Store.Index: %w", s.provider, err)
		}
	}()
	if err = request.Validate(); err != nil {
		return err
	}
	for _, doc := range request.Documents {
		if doc.Media != nil {
			return fmt.Errorf("%w: media is unsupported", vectorstore.ErrInvalidDocument)
		}
		if strings.ContainsRune(doc.Text, 0) {
			return fmt.Errorf("%w: PostgreSQL text cannot contain NUL", vectorstore.ErrInvalidDocument)
		}
	}
	batches, err := request.Batch(ctx, s.documentBatcher)
	if err != nil {
		return err
	}
	transaction, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer s.finishTransaction(ctx, transaction, &err)
	width, err := s.readSchema(ctx, transaction)
	if err != nil {
		return err
	}
	if _, err = s.readSource(ctx, transaction, width, false); err != nil {
		return err
	}
	prepared := &pgx.Batch{}
	statement := fmt.Sprintf("INSERT INTO %s (id,content,%s,embedding) VALUES ($1,$2,$3,$4) ON CONFLICT(id) DO UPDATE SET content=EXCLUDED.content,%s=EXCLUDED.%s,embedding=EXCLUDED.embedding", s.fullTable, s.metadataColumn, s.metadataColumn, s.metadataColumn)
	for _, batch := range batches {
		texts, err := batch.Texts()
		if err != nil {
			return err
		}
		vectors, err := s.embeddingClient.EmbedTexts(ctx, texts)
		if err != nil {
			return err
		}
		for index, doc := range batch.Documents {
			facts, err := doc.Metadata.MarshalJSON()
			if err != nil {
				return err
			}
			vector, err := s.distanceMetric.Vector(vectors[index], width)
			if err != nil {
				return err
			}
			prepared.Queue(statement, []byte(doc.ID), doc.Text, facts, vector)
		}
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

func (s *Store) Search(ctx context.Context, request *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("%s.Store.Search: %w", s.provider, err)
		}
	}()
	if err = request.Validate(); err != nil {
		return nil, err
	}
	if err = request.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
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
	records, err := s.readSource(ctx, transaction, width, false)
	if err != nil {
		return nil, err
	}
	ids, err := selectIDs(records, request.Options.Filter)
	if err != nil {
		return nil, err
	}
	response = &vectorstore.SearchResponse{Results: []*vectorstore.SearchResult{}}
	if len(ids) == 0 {
		return response, nil
	}
	vector, err := s.embeddingClient.EmbedText(ctx, request.Query)
	if err != nil {
		return nil, err
	}
	queryVector, err := s.distanceMetric.Vector(vector, width)
	if err != nil {
		return nil, err
	}
	statement := fmt.Sprintf("SELECT id,content,%s,embedding,embedding %s $2 AS distance FROM %s WHERE id=ANY($1) ORDER BY distance,id LIMIT $3", s.metadataColumn, s.distanceMetric.Operator(), s.fullTable)
	rows, err := transaction.Query(ctx, statement, ids, queryVector, request.Options.ResultLimit())
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
	if err = response.ValidateFor(request); err != nil {
		return nil, err
	}
	return response, nil
}

func (s *Store) DeleteWhere(ctx context.Context, predicate filter.Predicate) (err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("%s.Store.DeleteWhere: %w", s.provider, err)
		}
	}()
	if predicate == nil {
		return vectorstore.ErrMissingFilter
	}
	if err = predicate.Validate(); err != nil {
		return err
	}
	transaction, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer s.finishTransaction(ctx, transaction, &err)
	width, err := s.readSchema(ctx, transaction)
	if err != nil {
		return err
	}
	records, err := s.readSource(ctx, transaction, width, true)
	if err != nil {
		return err
	}
	ids, err := selectIDs(records, predicate)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	tag, err := transaction.Exec(ctx, "DELETE FROM "+s.fullTable+" WHERE id=ANY($1)", ids)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != int64(len(ids)) {
		return errors.New("pgstore: deletion did not acknowledge every locked identity")
	}
	return nil
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
	return width, primary.Err()
}

func (s *Store) readSource(ctx context.Context, transaction pgx.Tx, width int, lock bool) ([]*document.Document, error) {
	statement := fmt.Sprintf("SELECT id,content,%s,embedding FROM %s ORDER BY id", s.metadataColumn, s.fullTable)
	if lock {
		statement += " FOR UPDATE"
	}
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
	var meta metadata.Map
	if err := meta.UnmarshalJSON(facts); err != nil {
		return nil, err
	}
	doc := &document.Document{ID: string(id), Text: text, Metadata: meta}
	if err := (&vectorstore.IndexRequest{Documents: []*document.Document{doc}}).Validate(); err != nil {
		return nil, err
	}
	if strings.ContainsRune(text, 0) {
		return nil, errors.New("pgstore: native content contains NUL")
	}
	return doc, nil
}

func selectIDs(docs []*document.Document, predicate filter.Predicate) ([][]byte, error) {
	ids := make([][]byte, 0, len(docs))
	for _, doc := range docs {
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
		ids = append(ids, []byte(doc.ID))
	}
	return ids, nil
}
