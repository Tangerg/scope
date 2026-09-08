package mariadb

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// Provider is the stable backend name for host-side attribution.
const Provider = "MariaDB"

// Exported defaults keep constructor behavior visible and overridable.
const (
	DefaultTableName       = "vector_store"
	DefaultIDColumn        = "id"
	DefaultContentColumn   = "content"
	DefaultMetadataColumn  = "metadata"
	DefaultEmbeddingColumn = "embedding"
	DefaultDistanceMetric  = DistanceCosine
)

// DistanceMetric selects the vec_distance_<metric> function used at
// query time and the distance ordering MariaDB applies under the
// vector index.
type DistanceMetric string

const (
	// DistanceCosine — cosine distance. Default.
	DistanceCosine DistanceMetric = "cosine"

	// DistanceEuclidean — Euclidean (L2) distance.
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

// StoreConfig contains configuration options for the MariaDB vector
// store.
type StoreConfig struct {
	// DB is the database handle. Required. Use a *sql.DB built from
	// the github.com/go-sql-driver/mysql driver pointed at a MariaDB
	// 11.7+ instance with vector support enabled.
	DB *sql.DB

	// SchemaName is the optional schema (database) prefix. When
	// empty the connection's default database is used.
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

	// Dimensions sets the VECTOR column width. When zero and
	// InitializeSchema is true, the store probes EmbeddingModel.
	Dimensions int

	// DistanceMetric selects the distance function. Optional:
	// defaults to [DistanceCosine].
	DistanceMetric DistanceMetric

	// InitializeSchema, when true, creates the table + vector index
	// if they don't already exist.
	InitializeSchema bool
}

func (s StoreConfig) Validate() error {
	s.applyDefaults()
	if s.DB == nil {
		return errors.New("mariadb: DB is required")
	}
	if lo.IsNil(s.EmbeddingModel) {
		return errors.New("mariadb: EmbeddingModel is required")
	}
	if lo.IsNil(s.DocumentBatcher) {
		return errors.New("mariadb: DocumentBatcher is required")
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

// Store implements vector-store capabilities with the VECTOR column type and
// vec_distance_* functions introduced in MariaDB 11.6+.
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

// NewStore performs schema setup during construction, which is why it takes
// a context: a store returned before its table and vector index exist would
// fail on the first index rather than at wiring, where the misconfiguration
// actually is.
func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	config.applyDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}

	embeddingClient, err := embeddingclient.New(config.EmbeddingModel)
	if err != nil {
		return nil, fmt.Errorf("mariadb: create embedding client: %w", err)
	}

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
		embeddingClient: embeddingClient,
		documentBatcher: config.DocumentBatcher,
		dimensions:      config.Dimensions,
		distanceMetric:  config.DistanceMetric,
	}

	if err = store.initialize(ctx, config.InitializeSchema); err != nil {
		return nil, fmt.Errorf("mariadb: initialize store: %w", err)
	}
	return store, nil
}

// initialize resolves dimensionality and provisions the table when
// requested.
func (s *Store) initialize(ctx context.Context, initSchema bool) error {
	if !initSchema {
		return nil
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

	stmt := fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %s (
			%s VARCHAR(64) NOT NULL PRIMARY KEY,
			%s TEXT,
			%s JSON,
			%s VECTOR(%d) NOT NULL,
			VECTOR INDEX %s_idx (%s)
		) ENGINE=InnoDB`,
		s.fullTable,
		s.idColumn,
		s.contentColumn,
		s.metadataColumn,
		s.embeddingColumn, s.dimensions,
		s.tableName, s.embeddingColumn,
	)
	if _, err := s.db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("create table %s: %w", s.fullTable, err)
	}
	return nil
}

// Index embeds documents and upserts them into the vector table.
func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) (err error) {
	if validateErr := request.Validate(); validateErr != nil {
		return fmt.Errorf("mariadb.Store.Index: %w", validateErr)
	}

	var batches []*vectorstore.IndexRequest
	batches, err = request.Batch(ctx, s.documentBatcher)
	if err != nil {
		return fmt.Errorf("mariadb: batch documents: %w", err)
	}

	upsert := fmt.Sprintf(
		`INSERT INTO %s (%s, %s, %s, %s) VALUES (?, ?, ?, VEC_FromText(?)) `+
			`ON DUPLICATE KEY UPDATE %s = VALUES(%s), %s = VALUES(%s), %s = VALUES(%s)`,
		s.fullTable, s.idColumn, s.contentColumn, s.metadataColumn, s.embeddingColumn,
		s.contentColumn, s.contentColumn,
		s.metadataColumn, s.metadataColumn,
		s.embeddingColumn, s.embeddingColumn,
	)

	for _, batch := range batches {
		docs := batch.Documents
		texts, err := batch.Texts()
		if err != nil {
			return fmt.Errorf("vectorstore: project document text: %w", err)
		}
		vectors, err := s.embeddingClient.EmbedTexts(ctx, texts)
		if err != nil {
			return fmt.Errorf("mariadb: embed documents: %w", err)
		}

		stmt, err := s.db.PrepareContext(ctx, upsert)
		if err != nil {
			return fmt.Errorf("mariadb: prepare upsert: %w", err)
		}

		execErr := func() error {
			defer stmt.Close()
			for i, doc := range docs {
				id := doc.ID
				metaJSON, err := marshalMetadata(doc.Metadata)
				if err != nil {
					return fmt.Errorf("marshal metadata for %s: %w", id, err)
				}
				vectorJSON, err := json.Marshal(embedding.Float32Vector(vectors[i]))
				if err != nil {
					return fmt.Errorf("mariadb: marshal vector for %s: %w", id, err)
				}
				if _, err := stmt.ExecContext(ctx, id, doc.Text, metaJSON, string(vectorJSON)); err != nil {
					return fmt.Errorf("upsert %s: %w", id, err)
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

// Search embeds the query, ranks rows by vec_distance, and returns
// matching documents above MinScore.
func (s *Store) Search(ctx context.Context, req *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	var docs []*vectorstore.SearchResult
	if err = req.Validate(); err != nil {
		return nil, fmt.Errorf("mariadb.Store.Search: %w", err)
	}
	if err = req.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, fmt.Errorf("mariadb.Store.Search: %w", err)
	}

	defer func() {
		if err == nil {
			err = response.ValidateFor(req)
		}
	}()

	vector, err := s.embeddingClient.EmbedText(ctx, req.Query)
	if err != nil {
		return nil, fmt.Errorf("mariadb: embed query: %w", err)
	}
	vectorJSON, err := json.Marshal(embedding.Float32Vector(vector))
	if err != nil {
		return nil, fmt.Errorf("mariadb: marshal query vector: %w", err)
	}
	vecText := string(vectorJSON)

	wherePredicate, whereArgs, err := s.buildFilter(req.Options.Filter)
	if err != nil {
		return nil, err
	}

	wherePart := ""
	if wherePredicate != "" {
		wherePart = " AND " + wherePredicate
	}

	stmt := fmt.Sprintf(
		`SELECT %s, %s, %s, vec_distance_%s(%s, VEC_FromText(?)) AS distance `+
			`FROM %s WHERE 1=1%s ORDER BY distance ASC LIMIT ?`,
		s.idColumn, s.contentColumn, s.metadataColumn,
		s.distanceMetric, s.embeddingColumn,
		s.fullTable, wherePart,
	)

	args := []any{vecText}
	args = append(args, whereArgs...)
	args = append(args, req.Options.ResultLimit())

	rows, err := s.db.QueryContext(ctx, stmt, args...)
	if err != nil {
		return nil, fmt.Errorf("mariadb: query %s: %w", s.fullTable, err)
	}
	defer rows.Close()

	docs = make([]*vectorstore.SearchResult, 0, req.Options.ResultLimit())
	for rows.Next() {
		var (
			id       string
			content  sql.NullString
			metaRaw  sql.NullString
			distance float64
		)
		if err = rows.Scan(&id, &content, &metaRaw, &distance); err != nil {
			return nil, fmt.Errorf("mariadb: scan row: %w", err)
		}

		score := s.distanceMetric.score(distance)
		if score < req.Options.MinScore {
			continue
		}
		if id == "" {
			return nil, errors.New("mariadb: search result is missing document ID")
		}
		if !content.Valid || content.String == "" {
			return nil, fmt.Errorf("mariadb: document %q is missing text", id)
		}

		doc := &document.Document{ID: id, Text: content.String}
		if metaRaw.Valid {
			if doc.Metadata, err = unmarshalMetadata([]byte(metaRaw.String)); err != nil {
				return nil, fmt.Errorf("mariadb: unmarshal metadata for %s: %w", id, err)
			}
		}
		docs = append(docs, &vectorstore.SearchResult{Document: doc, Score: score})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mariadb: read rows: %w", err)
	}
	return &vectorstore.SearchResponse{Results: docs}, nil
}

func (s *Store) DeleteWhere(ctx context.Context, expr filter.Predicate) (err error) {
	if expr == nil {
		return vectorstore.ErrMissingFilter
	}
	if err = expr.Validate(); err != nil {
		return fmt.Errorf("mariadb.Store.DeleteWhere: %w", err)
	}

	var (
		predicate string
		args      []any
	)
	predicate, args, err = s.buildFilter(expr)
	if err != nil {
		return err
	}
	if predicate == "" {
		return errors.New("mariadb: refusing to delete on empty filter")
	}

	stmt := fmt.Sprintf("DELETE FROM %s WHERE %s", s.fullTable, predicate)
	if _, err := s.db.ExecContext(ctx, stmt, args...); err != nil {
		return fmt.Errorf("mariadb: delete from %s: %w", s.fullTable, err)
	}
	return nil
}

// DeleteIDs removes rows by primary key. MariaDB has no array type,
// so it emits one `?` placeholder per id —
// `DELETE FROM <table> WHERE <id> IN (?, ?, ...)` — binding the ids as
// query args. An empty slice is a no-op; unknown ids are silently
// ignored (idempotent). Implements [vectorstore.IDDeleter].
func (s *Store) DeleteIDs(ctx context.Context, ids []string) (err error) {
	if len(ids) == 0 {
		return nil
	}

	placeholders := strings.Repeat("?, ", len(ids)-1) + "?"
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}

	stmt := fmt.Sprintf("DELETE FROM %s WHERE %s IN (%s)", s.fullTable, s.idColumn, placeholders)
	if _, err = s.db.ExecContext(ctx, stmt, args...); err != nil {
		return fmt.Errorf("mariadb: delete by ids from %s: %w", s.fullTable, err)
	}
	return nil
}

// buildFilter wraps the visitor.
func (s *Store) buildFilter(expr filter.Predicate) (string, []any, error) {
	if expr == nil {
		return "", nil, nil
	}
	v := newVisitor(s.metadataColumn)
	if err := expr.Accept(v); err != nil {
		return "", nil, fmt.Errorf("mariadb: convert filter: %w", err)
	}
	predicate, args := v.snapshot()
	return predicate, args, nil
}

func marshalMetadata(m metadata.Map) ([]byte, error) {
	if m == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(m)
}

func unmarshalMetadata(b []byte) (metadata.Map, error) {
	if len(b) == 0 {
		return nil, nil
	}
	var out metadata.Map
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}
