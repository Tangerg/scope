// Package pgstore implements the shared pgwire execution semantics used by
// PostgreSQL/pgvector and CockroachDB. Provider packages remain responsible for
// configuration validation and schema provisioning.
package pgstore

import (
	"context"
	"encoding/json"
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
func (s *Store) Search(ctx context.Context, req *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	var docs []*vectorstore.SearchResult
	if err = req.Validate(); err != nil {
		return nil, fmt.Errorf("%s.Store.Search: %w", s.provider, err)
	}
	if err = req.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, fmt.Errorf("%s.Store.Search: %w", s.provider, err)
	}

	defer func() {
		if err == nil {
			err = response.ValidateFor(req)
		}
	}()

	vector, err := s.embeddingClient.EmbedText(ctx, req.Query)
	if err != nil {
		return nil, fmt.Errorf("%s.Store.Search: embed query: %w", s.provider, err)
	}
	queryVec := pgvec.NewVector(embedding.Float32Vector(vector))

	whereSQL, args, err := s.buildWhereClause(req.Options.Filter)
	if err != nil {
		return nil, err
	}

	args = append(args, queryVec)
	distancePlaceholder := fmt.Sprintf("$%d", len(args))
	args = append(args, req.Options.ResultLimit())
	limitPlaceholder := fmt.Sprintf("$%d", len(args))

	sql := fmt.Sprintf(
		`SELECT id, content, %s, embedding %s %s AS distance FROM %s%s ORDER BY distance LIMIT %s`,
		s.metadataColumn, s.distanceMetric.operator(), distancePlaceholder,
		s.fullTable, whereSQL, limitPlaceholder,
	)

	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("%s.Store.Search: query %s: %w", s.provider, s.fullTable, err)
	}
	defer rows.Close()

	docs = make([]*vectorstore.SearchResult, 0, req.Options.ResultLimit())
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
		if score < req.Options.MinScore {
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

func (s *Store) DeleteWhere(ctx context.Context, expr filter.Predicate) (err error) {
	if expr == nil {
		return vectorstore.ErrMissingFilter
	}
	if err = expr.Validate(); err != nil {
		return fmt.Errorf("%s.Store.DeleteWhere: %w", s.provider, err)
	}

	var (
		fragment string
		args     []any
	)
	fragment, args, err = s.buildWhereClause(expr)
	if err != nil {
		return err
	}
	if fragment == "" {
		return fmt.Errorf("%s.Store.DeleteWhere: filter produced no SQL predicate", s.provider)
	}

	sql := fmt.Sprintf(`DELETE FROM %s%s`, s.fullTable, fragment)
	if _, err = s.pool.Exec(ctx, sql, args...); err != nil {
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

// buildWhereClause converts the optional filter expression into a SQL
// fragment (prefixed with " WHERE ") and the matching argument slice.
// Returns ("", nil, nil) when filter is nil.
func (s *Store) buildWhereClause(expr filter.Predicate) (string, []any, error) {
	if expr == nil {
		return "", nil, nil
	}
	compiler := pgfilter.NewCompiler(s.metadataColumn)
	if err := expr.Accept(compiler); err != nil {
		return "", nil, fmt.Errorf("%s: compile metadata filter: %w", s.provider, err)
	}
	fragment, args := compiler.Result()
	if fragment == "" {
		return "", nil, nil
	}
	return " WHERE " + fragment, args, nil
}

// marshalMetadata serializes the document metadata into the JSON bytes
// stored in the jsonb column. nil maps round-trip as JSON null.
func marshalMetadata(m metadata.Map) ([]byte, error) {
	if m == nil {
		return []byte("null"), nil
	}
	return json.Marshal(m)
}

// unmarshalMetadata reverses marshalMetadata. NULL jsonb columns
// produce a nil map.
func unmarshalMetadata(b []byte) (metadata.Map, error) {
	if len(b) == 0 || string(b) == "null" {
		return nil, nil
	}
	var out metadata.Map
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}
