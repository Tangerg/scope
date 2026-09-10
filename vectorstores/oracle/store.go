package oracle

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

const firstMetadataFilterBindIndex = 2

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

	// DistanceDot — dot product. Oracle returns the raw inner
	// product; the store wraps it as `(1 + dot) / 2` so scores stay
	// in [0, 1] for unit-norm vectors.
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
	// When empty the connection user's default schema is used.
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

	// Dimensions sets the VECTOR column width, and is required when
	// InitializeSchema is true: the width is part of the column type, and
	// nothing here can read it off a table that does not exist yet. It is also
	// what TO_VECTOR is told at query time.
	Dimensions int

	// DistanceMetric selects the distance function. Optional:
	// defaults to [DistanceCosine].
	DistanceMetric DistanceMetric

	// InitializeSchema, when true, creates the table if it doesn't
	// already exist.
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
		return nil, fmt.Errorf("oracle: create embedding client: %w", err)
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
		return nil, fmt.Errorf("oracle: initialize store: %w", err)
	}
	return store, nil
}

// initialize provisions the table.
func (s *Store) initialize(ctx context.Context, initSchema bool) error {
	if !initSchema {
		return nil
	}
	if s.dimensions <= 0 {
		return errors.New("oracle: Dimensions must be > 0")
	}

	createSQL := fmt.Sprintf(
		`CREATE TABLE %s (
			%s VARCHAR2(64) PRIMARY KEY,
			%s CLOB,
			%s JSON,
			%s VECTOR(%d, FLOAT32)
		)`,
		s.fullTable,
		s.idColumn,
		s.contentColumn,
		s.metadataColumn,
		s.embeddingColumn, s.dimensions,
	)
	if _, err := s.db.ExecContext(ctx, createSQL); err != nil {
		// Oracle returns ORA-00955 when the table already exists, and has no
		// CREATE TABLE IF NOT EXISTS to ask for that outcome directly.
		//
		// The code is matched as text rather than through a typed driver error
		// because this store takes a *sql.DB and holds no Oracle driver, so
		// the caller chooses one and its error types are not visible here. The
		// ORA-NNNNN prefix survives an NLS_LANGUAGE that translates the
		// message after it, which is why the code and not the wording is what
		// this looks for.
		if !strings.Contains(err.Error(), "ORA-00955") {
			return fmt.Errorf("create table %s: %w", s.fullTable, err)
		}
	}
	return nil
}

// Index embeds documents and upserts them via MERGE.
func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) (err error) {
	if validateErr := request.Validate(); validateErr != nil {
		return fmt.Errorf("oracle.Store.Index: %w", validateErr)
	}
	for index, doc := range request.Documents {
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
		`MERGE INTO %s tgt USING (SELECT :1 AS id, :2 AS content, :3 AS metadata, TO_VECTOR(:4, %d, FLOAT32) AS embedding FROM dual) src `+
			`ON (tgt.%s = src.id) `+
			`WHEN MATCHED THEN UPDATE SET tgt.%s = src.content, tgt.%s = src.metadata, tgt.%s = src.embedding `+
			`WHEN NOT MATCHED THEN INSERT (tgt.%s, tgt.%s, tgt.%s, tgt.%s) VALUES (src.id, src.content, src.metadata, src.embedding)`,
		s.fullTable, s.dimensions,
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
					return fmt.Errorf("oracle: marshal vector for %s: %w", id, err)
				}
				if _, err := stmt.ExecContext(ctx, id, doc.Text, string(metaJSON), string(vectorJSON)); err != nil {
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

// Search runs VECTOR_DISTANCE against the embedding column.
func (s *Store) Search(ctx context.Context, req *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	var docs []*vectorstore.SearchResult
	if err = req.Validate(); err != nil {
		return nil, fmt.Errorf("oracle.Store.Search: %w", err)
	}
	if err = req.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, fmt.Errorf("oracle.Store.Search: %w", err)
	}

	defer func() {
		if err == nil {
			err = response.ValidateFor(req)
		}
	}()

	vector, err := s.embeddingClient.EmbedText(ctx, req.Query)
	if err != nil {
		return nil, fmt.Errorf("oracle: embed query: %w", err)
	}
	vectorJSON, err := json.Marshal(embedding.Float32Vector(vector))
	if err != nil {
		return nil, fmt.Errorf("oracle: marshal query vector: %w", err)
	}
	vecText := string(vectorJSON)

	wherePredicate, whereArgs, err := s.buildFilter(req.Options.Filter, firstMetadataFilterBindIndex)
	if err != nil {
		return nil, err
	}
	wherePart := ""
	if wherePredicate != "" {
		wherePart = " WHERE " + wherePredicate
	}

	// Oracle DOT distance returns the inner product directly; wrap so
	// the LIMIT/ORDER still picks "closest first".
	distanceExpr := fmt.Sprintf("VECTOR_DISTANCE(%s, TO_VECTOR(:1, %d, FLOAT32), %s)",
		s.embeddingColumn, s.dimensions, s.distanceMetric)

	limitArgIdx := firstMetadataFilterBindIndex + len(whereArgs)
	stmt := fmt.Sprintf(
		`SELECT %s, %s, %s, %s AS distance FROM %s%s ORDER BY distance FETCH FIRST :%d ROWS ONLY`,
		s.idColumn, s.contentColumn, s.metadataColumn,
		distanceExpr, s.fullTable, wherePart, limitArgIdx,
	)

	args := []any{vecText}
	args = append(args, whereArgs...)
	args = append(args, req.Options.ResultLimit())

	rows, err := s.db.QueryContext(ctx, stmt, args...)
	if err != nil {
		return nil, fmt.Errorf("oracle: query %s: %w", s.fullTable, err)
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
			return nil, fmt.Errorf("oracle: scan row: %w", err)
		}

		score := s.distanceMetric.score(distance)
		if score < req.Options.MinScore {
			continue
		}
		if id == "" {
			return nil, errors.New("oracle: search result is missing document ID")
		}
		if !content.Valid || content.String == "" {
			return nil, fmt.Errorf("oracle: document %q is missing text", id)
		}

		doc := &document.Document{ID: id, Text: content.String}
		if metaRaw.Valid {
			if doc.Metadata, err = unmarshalMetadata([]byte(metaRaw.String)); err != nil {
				return nil, fmt.Errorf("oracle: unmarshal metadata for %s: %w", id, err)
			}
		}
		docs = append(docs, &vectorstore.SearchResult{Document: doc, Score: score})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("oracle: read rows: %w", err)
	}
	return &vectorstore.SearchResponse{Results: docs}, nil
}

func (s *Store) DeleteWhere(ctx context.Context, expr filter.Predicate) (err error) {
	if expr == nil {
		return vectorstore.ErrMissingFilter
	}
	if err = expr.Validate(); err != nil {
		return fmt.Errorf("oracle.Store.DeleteWhere: %w", err)
	}

	var (
		predicate string
		args      []any
	)
	predicate, args, err = s.buildFilter(expr, 1)
	if err != nil {
		return err
	}
	if predicate == "" {
		return errors.New("oracle: refusing to delete on empty filter")
	}

	stmt := fmt.Sprintf("DELETE FROM %s WHERE %s", s.fullTable, predicate)
	if _, err := s.db.ExecContext(ctx, stmt, args...); err != nil {
		return fmt.Errorf("oracle: delete from %s: %w", s.fullTable, err)
	}
	return nil
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
		args[i] = id
	}

	stmt := fmt.Sprintf("DELETE FROM %s WHERE %s IN (%s)",
		s.fullTable, s.idColumn, strings.Join(placeholders, ", "))
	if _, err = s.db.ExecContext(ctx, stmt, args...); err != nil {
		return fmt.Errorf("oracle: delete by ids from %s: %w", s.fullTable, err)
	}
	return nil
}

// buildFilter wraps the visitor and renumbers placeholders so they
// continue from startIdx — Oracle uses positional `:N` bindings, so
// the search path that prepends the query-vector parameter at `:1`
// must skip ahead.
func (s *Store) buildFilter(expr filter.Predicate, startIdx int) (string, []any, error) {
	if expr == nil {
		return "", nil, nil
	}
	v := newVisitor(s.metadataColumn)
	if err := expr.Accept(v); err != nil {
		return "", nil, fmt.Errorf("oracle: convert filter: %w", err)
	}
	predicate, args := v.snapshot()
	if startIdx > 1 && len(args) > 0 {
		predicate = renumberPlaceholders(predicate, startIdx)
	}
	return predicate, args, nil
}

// renumberPlaceholders shifts every `:N` placeholder in fragment by
// (offset - 1). The visitor produces `:1`, `:2`, … starting from 1;
// when the call site has already consumed some bind positions we
// rewrite them so the executed SQL matches the args slice.
func renumberPlaceholders(fragment string, offset int) string {
	var b strings.Builder
	b.Grow(len(fragment))
	i := 0
	for i < len(fragment) {
		if fragment[i] == ':' && i+1 < len(fragment) && fragment[i+1] >= '0' && fragment[i+1] <= '9' {
			j := i + 1
			for j < len(fragment) && fragment[j] >= '0' && fragment[j] <= '9' {
				j++
			}
			// The scan above already established fragment[i+1:j] is a
			// non-empty run of ASCII digits, and the visitor numbers one
			// placeholder per bound argument, so the only way Atoi could fail
			// is a digit run wider than an int — unreachable for this input.
			n, _ := strconv.Atoi(fragment[i+1 : j])
			b.WriteByte(':')
			b.WriteString(strconv.Itoa(n + offset - 1))
			i = j
			continue
		}
		b.WriteByte(fragment[i])
		i++
	}
	return b.String()
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
