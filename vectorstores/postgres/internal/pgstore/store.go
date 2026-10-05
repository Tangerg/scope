// Package pgstore implements the shared pgwire execution semantics used by
// PostgreSQL/pgvector and CockroachDB. Provider packages remain responsible for
// configuration validation and schema provisioning.
package pgstore

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pgvec "github.com/pgvector/pgvector-go"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/vectorstores/postgres/internal/pgstore/pgfilter"
)

// DistanceMetric selects the pgvector-compatible query operator.
type DistanceMetric string

const (
	DistanceCosine DistanceMetric = "cosine"
	DistanceL2     DistanceMetric = "l2"
	DistanceIP     DistanceMetric = "ip"
)

func (d DistanceMetric) Valid() bool {
	switch d {
	case DistanceCosine, DistanceL2, DistanceIP:
		return true
	default:
		return false
	}
}

func (d DistanceMetric) String() string { return string(d) }

// Config contains only shared execution dependencies. Schema policy belongs to
// the provider package and must be resolved before constructing the engine.
type Config struct {
	Provider        string
	Pool            *pgxpool.Pool
	SchemaName      string
	TableName       string
	MetadataColumn  string
	EmbeddingModel  embedding.Model
	DocumentBatcher vectorstore.Batcher
	DistanceMetric  DistanceMetric
}

var (
	_ vectorstore.Indexer       = (*Store)(nil)
	_ vectorstore.Searcher      = (*Store)(nil)
	_ vectorstore.FilterDeleter = (*Store)(nil)
	_ vectorstore.IDDeleter     = (*Store)(nil)
)

// Store executes pgvector-compatible data operations for one named provider.
type Store struct {
	provider        string
	pool            *pgxpool.Pool
	metadataColumn  string
	fullTable       string
	embeddingClient embeddingclient.Client
	documentBatcher vectorstore.Batcher
	distanceMetric  DistanceMetric
}

// New builds the shared PostgreSQL-dialect store that pgvector, CockroachDB,
// and other wire-compatible backends delegate to. The provider name is passed
// in so errors name the module the caller actually imported instead of this
// internal one.
func New(config Config) (Store, error) {
	if !config.DistanceMetric.Valid() {
		return Store{}, fmt.Errorf("%s: unsupported distance metric %q", config.Provider, config.DistanceMetric)
	}
	embeddingClient, err := embeddingclient.New(config.EmbeddingModel)
	if err != nil {
		return Store{}, fmt.Errorf("%s: create embedding client: %w", config.Provider, err)
	}

	return Store{
		provider:        config.Provider,
		pool:            config.Pool,
		metadataColumn:  config.MetadataColumn,
		fullTable:       config.SchemaName + "." + config.TableName,
		embeddingClient: embeddingClient,
		documentBatcher: config.DocumentBatcher,
		distanceMetric:  config.DistanceMetric,
	}, nil
}

// operator returns the pgvector binary operator used by ORDER BY for
// this distance metric.
func (d DistanceMetric) operator() string {
	switch d {
	case DistanceL2:
		return "<->"
	case DistanceIP:
		return "<#>"
	case DistanceCosine:
		fallthrough
	default:
		return "<=>"
	}
}

func (d DistanceMetric) score(distance float64) vectorstore.Score {
	switch d {
	case DistanceL2:
		return vectorstore.ScoreFromDistance(distance)
	case DistanceIP:
		return vectorstore.ScoreFromNegativeInnerProductDistance(distance)
	case DistanceCosine:
		fallthrough
	default:
		return vectorstore.ScoreFromCosineDistance(distance)
	}
}

// Index embeds the documents and upserts them into the configured table.
func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) (err error) {
	// Reject unsupported content before consulting the batcher. Batch owns
	// structural validation, including missing requests and documents.
	if request != nil {
		for index, doc := range request.Documents {
			if doc != nil && doc.Media != nil {
				return fmt.Errorf("%s.Store.Index: %w: documents[%d] contains unsupported media", s.provider, vectorstore.ErrInvalidDocument, index)
			}
		}
	}

	var batches []*vectorstore.IndexRequest
	batches, err = request.Batch(ctx, s.documentBatcher)
	if err != nil {
		return fmt.Errorf("%s.Store.Index: batch documents: %w", s.provider, err)
	}

	upsertSQL := fmt.Sprintf(
		`INSERT INTO %s (id, content, %s, embedding) VALUES ($1, $2, $3::jsonb, $4)
		 ON CONFLICT (id) DO UPDATE SET
		   content   = EXCLUDED.content,
		   %s        = EXCLUDED.%s,
		   embedding = EXCLUDED.embedding`,
		s.fullTable, s.metadataColumn, s.metadataColumn, s.metadataColumn,
	)

	for _, batch := range batches {
		docs := batch.Documents
		texts, err := batch.Texts()
		if err != nil {
			return fmt.Errorf("vectorstore: project document text: %w", err)
		}
		vectors, err := s.embeddingClient.EmbedTexts(ctx, texts)
		if err != nil {
			return fmt.Errorf("%s.Store.Index: embed documents: %w", s.provider, err)
		}

		batch := &pgx.Batch{}
		for i, doc := range docs {
			id := doc.ID

			metaJSON, err := marshalMetadata(doc.Metadata)
			if err != nil {
				return fmt.Errorf("%s.Store.Index: marshal metadata for document %q: %w", s.provider, id, err)
			}

			vec := pgvec.NewVector(embedding.Float32Vector(vectors[i]))
			batch.Queue(upsertSQL, id, doc.Text, metaJSON, vec)
		}

		results := s.pool.SendBatch(ctx, batch)
		execErr := drainBatch(results, len(docs))
		closeErr := results.Close()
		if execErr != nil {
			return fmt.Errorf("%s.Store.Index: execute upsert batch: %w", s.provider, execErr)
		}
		if closeErr != nil {
			return fmt.Errorf("%s.Store.Index: close upsert batch: %w", s.provider, closeErr)
		}
	}
	return nil
}

// drainBatch consumes every queued statement's tag.
//
// Close would drain too — pgx reads and executes every remaining queued query
// before returning its first error — so this is not what keeps the connection
// consistent. It is what separates "a statement failed" from "closing the
// batch failed", which the two distinct errors below report.
func drainBatch(br pgx.BatchResults, n int) error {
	for range n {
		if _, err := br.Exec(); err != nil {
			return err
		}
	}
	return nil
}

// Search embeds the query, runs an ANN search, and returns the matching
// documents above the configured MinScore threshold.
func (s *Store) Search(ctx context.Context, request *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	var docs []*vectorstore.SearchResult
	if err = request.Validate(); err != nil {
		return nil, fmt.Errorf("%s.Store.Search: %w", s.provider, err)
	}
	if err = request.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, fmt.Errorf("%s.Store.Search: %w", s.provider, err)
	}

	defer func() {
		if err == nil {
			err = response.ValidateFor(request)
		}
		if err != nil {
			response = nil
		}
	}()

	compiled, transaction, err := s.prepareFilter(ctx, request.Options.Filter, pgx.ReadOnly)
	if err != nil {
		return nil, err
	}
	if transaction != nil {
		defer s.finishTransaction(ctx, transaction, &err)
	}

	vector, err := s.embeddingClient.EmbedText(ctx, request.Query)
	if err != nil {
		return nil, fmt.Errorf("%s.Store.Search: embed query: %w", s.provider, err)
	}
	queryVec := pgvec.NewVector(embedding.Float32Vector(vector))

	whereSQL := ""
	if compiled.Predicate != "" {
		whereSQL = " WHERE " + compiled.Predicate
	}
	args := compiled.Args

	args = append(args, queryVec)
	distancePlaceholder := fmt.Sprintf("$%d", len(args))
	args = append(args, request.Options.ResultLimit())
	limitPlaceholder := fmt.Sprintf("$%d", len(args))

	sql := fmt.Sprintf(
		`SELECT id, content, %s, embedding %s %s AS distance FROM %s%s ORDER BY distance LIMIT %s`,
		s.metadataColumn, s.distanceMetric.operator(), distancePlaceholder,
		s.fullTable, whereSQL, limitPlaceholder,
	)

	query := s.pool.Query
	if transaction != nil {
		query = transaction.Query
	}
	rows, err := query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("%s.Store.Search: query %s: %w", s.provider, s.fullTable, err)
	}
	defer rows.Close()

	docs = make([]*vectorstore.SearchResult, 0, request.Options.ResultLimit())
	for rows.Next() {
		var (
			id       string
			content  *string
			metaRaw  []byte
			distance float64
		)
		if err = rows.Scan(&id, &content, &metaRaw, &distance); err != nil {
			return nil, fmt.Errorf("%s.Store.Search: scan row: %w", s.provider, err)
		}

		score := s.distanceMetric.score(distance)
		if score < request.Options.MinScore {
			continue
		}
		if id == "" {
			return nil, fmt.Errorf("%s.Store.Search: row is missing document ID", s.provider)
		}
		if content == nil || *content == "" {
			return nil, fmt.Errorf("%s.Store.Search: document %q is missing text", s.provider, id)
		}

		doc := &document.Document{ID: id, Text: *content}
		if len(metaRaw) > 0 {
			if doc.Metadata, err = unmarshalMetadata(metaRaw); err != nil {
				return nil, fmt.Errorf("%s.Store.Search: unmarshal metadata for %q: %w", s.provider, id, err)
			}
		}
		docs = append(docs, &vectorstore.SearchResult{Document: doc, Score: score})
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("%s.Store.Search: read rows: %w", s.provider, err)
	}
	return &vectorstore.SearchResponse{Results: docs}, nil
}

// DeleteWhere removes every row whose metadata matches the predicate.
func (s *Store) DeleteWhere(ctx context.Context, predicate filter.Predicate) (err error) {
	if predicate == nil {
		return vectorstore.ErrMissingFilter
	}
	if err = predicate.Validate(); err != nil {
		return fmt.Errorf("%s.Store.DeleteWhere: %w", s.provider, err)
	}

	compiled, transaction, err := s.prepareFilter(ctx, predicate, pgx.ReadWrite)
	if err != nil {
		return err
	}
	if transaction != nil {
		defer s.finishTransaction(ctx, transaction, &err)
	}
	if compiled.Predicate == "" {
		return fmt.Errorf("%s.Store.DeleteWhere: filter produced no SQL predicate", s.provider)
	}

	sql := fmt.Sprintf(`DELETE FROM %s WHERE %s`, s.fullTable, compiled.Predicate)
	exec := s.pool.Exec
	if transaction != nil {
		exec = transaction.Exec
	}
	if _, err = exec(ctx, sql, compiled.Args...); err != nil {
		return fmt.Errorf("%s.Store.DeleteWhere: delete from %s: %w", s.provider, s.fullTable, err)
	}
	return nil
}

// DeleteIDs removes rows by primary key — `DELETE ... WHERE id = ANY($1)`.
// pgx maps the []string to a Postgres text array. An empty slice is a
// no-op; unknown ids are silently ignored (idempotent). Implements
// [vectorstore.IDDeleter].
func (s *Store) DeleteIDs(ctx context.Context, ids []string) (err error) {
	if len(ids) == 0 {
		return nil
	}

	sql := fmt.Sprintf(`DELETE FROM %s WHERE id = ANY($1)`, s.fullTable)
	if _, err = s.pool.Exec(ctx, sql, ids); err != nil {
		return fmt.Errorf("%s.Store.DeleteIDs: delete from %s: %w", s.provider, s.fullTable, err)
	}
	return nil
}

// Error validation and the filtered operation share a snapshot. Otherwise an
// intervening write could turn a valid predicate into a silently omitted error.
// A successful return transfers transaction finalization to the operation.
func (s *Store) prepareFilter(ctx context.Context, predicate filter.Predicate, accessMode pgx.TxAccessMode) (compiled pgfilter.Query, transaction pgx.Tx, err error) {
	if predicate == nil {
		return pgfilter.Query{}, nil, nil
	}
	compiler := pgfilter.NewCompiler(s.metadataColumn)
	if err = predicate.Accept(compiler); err != nil {
		return pgfilter.Query{}, nil, fmt.Errorf("%s: compile metadata filter: %w", s.provider, err)
	}
	compiled = compiler.Result()
	if compiled.Invalid == "" {
		return compiled, nil, nil
	}
	transaction, err = s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable, AccessMode: accessMode})
	if err != nil {
		return pgfilter.Query{}, nil, fmt.Errorf("%s: begin metadata filter transaction: %w", s.provider, err)
	}
	defer func() {
		if err != nil {
			s.finishTransaction(ctx, transaction, &err)
		}
	}()
	// Selecting the compiled predicate gives every bound parameter its SQL type,
	// including literals not needed to identify the invalid row.
	sql := fmt.Sprintf("SELECT id, %s, (%s) FROM %s WHERE %s LIMIT 1", s.metadataColumn, compiled.Predicate, s.fullTable, compiled.Invalid)
	var id string
	var raw []byte
	var selected bool
	err = transaction.QueryRow(ctx, sql, compiled.Args...).Scan(&id, &raw, &selected)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
		return compiled, transaction, nil
	}
	if err != nil {
		return compiled, transaction, fmt.Errorf("%s: validate metadata filter: %w", s.provider, err)
	}
	if selected {
		return compiled, transaction, fmt.Errorf("%s: metadata filter selected invalid document %q", s.provider, id)
	}
	facts, err := unmarshalMetadata(raw)
	if err != nil {
		return compiled, transaction, fmt.Errorf("%s: decode metadata for filter validation on document %q: %w", s.provider, id, err)
	}
	values, err := facts.Values()
	if err != nil {
		return compiled, transaction, fmt.Errorf("%s: decode filter values on document %q: %w", s.provider, id, err)
	}
	if _, err = filter.Match(predicate, values); err != nil {
		return compiled, transaction, fmt.Errorf("%s: evaluate metadata filter on document %q: %w", s.provider, id, err)
	}
	return compiled, transaction, fmt.Errorf("%s: metadata filter error condition disagrees with Core on document %q", s.provider, id)
}

func (s *Store) finishTransaction(ctx context.Context, transaction pgx.Tx, operationErr *error) {
	if *operationErr == nil {
		if err := transaction.Commit(ctx); err != nil {
			*operationErr = fmt.Errorf("%s: commit metadata filter transaction: %w", s.provider, err)
		}
	}
	if err := transaction.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		*operationErr = errors.Join(*operationErr, fmt.Errorf("%s: roll back metadata filter transaction: %w", s.provider, err))
	}
}

// marshalMetadata serializes the document metadata into the JSON bytes
// stored in the jsonb column. nil maps round-trip as JSON null.
func marshalMetadata(m metadata.Map) ([]byte, error) {
	if m == nil {
		return []byte("null"), nil
	}
	return jsonv2.Marshal(m)
}

// unmarshalMetadata reverses marshalMetadata. NULL jsonb columns
// produce a nil map.
func unmarshalMetadata(b []byte) (metadata.Map, error) {
	if len(b) == 0 || string(b) == "null" {
		return nil, nil
	}
	var out metadata.Map
	if err := jsonv2.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}
