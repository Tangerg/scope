package pgvector

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Tangerg/scope/vectorstores/postgres"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/vectorstores/postgres/internal/pgstore"
)

const Provider = "PgVector"

const (
	DefaultSchemaName     = "public"
	DefaultTableName      = "vector_store"
	DefaultMetadataColumn = "metadata"
	DefaultIndexSuffix    = "_embedding_idx"
)

type IndexType string

const (
	IndexHNSW    IndexType = "hnsw"
	IndexIVFFlat IndexType = "ivfflat"
	IndexNone    IndexType = "none"
)

func (i IndexType) Valid() bool {
	switch i {
	case IndexHNSW, IndexIVFFlat, IndexNone:
		return true
	default:
		return false
	}
}

func (i IndexType) String() string { return string(i) }

type StoreConfig struct {
	Pool           *pgxpool.Pool
	SchemaName     string
	TableName      string
	IndexName      string
	MetadataColumn string
	// Space is the Host-owned immutable binding for this physical collection.
	Space vectorstore.SpaceID
	// Dimensions seeds only a newly created native column; existing native width is authoritative.
	Dimensions       int
	DistanceMetric   postgres.DistanceMetric
	IndexType        IndexType
	InitializeSchema bool
}

func (s StoreConfig) Validate() error {
	s.applyDefaults()
	if s.Pool == nil {
		return errors.New("pgvector: Pool is required")
	}
	if err := s.Space.Validate(); err != nil {
		return err
	}

	if s.Dimensions < 0 {
		return errors.New("pgvector: Dimensions must be >= 0")
	}
	if !s.DistanceMetric.Valid() {
		return fmt.Errorf("pgvector: unsupported DistanceMetric %q", s.DistanceMetric)
	}
	if !s.IndexType.Valid() {
		return fmt.Errorf("pgvector: unsupported IndexType %q", s.IndexType)
	}
	return s.validateIdentifiers()
}

func (s StoreConfig) validateIdentifiers() error {
	switch s.MetadataColumn {
	case "id", "content", "embedding":
		return errors.New("pgvector: MetadataColumn conflicts with a document column")
	}
	if err := identifier(s.SchemaName).validate("SchemaName"); err != nil {
		return err
	}
	if err := identifier(s.TableName).validate("TableName"); err != nil {
		return err
	}
	if err := identifier(s.IndexName).validate("IndexName"); err != nil {
		return err
	}
	return identifier(s.MetadataColumn).validate("MetadataColumn")
}

func (s *StoreConfig) applyDefaults() {
	s.SchemaName = cmp.Or(s.SchemaName, DefaultSchemaName)
	s.TableName = cmp.Or(s.TableName, DefaultTableName)
	s.MetadataColumn = cmp.Or(s.MetadataColumn, DefaultMetadataColumn)
	if s.IndexName == "" {
		s.IndexName = s.TableName + DefaultIndexSuffix
	}
	s.DistanceMetric = cmp.Or(s.DistanceMetric, postgres.DistanceCosine)
	s.IndexType = cmp.Or(s.IndexType, IndexHNSW)
}

var (
	_ vectorstore.Upserter  = (*Store)(nil)
	_ vectorstore.Querier   = (*Store)(nil)
	_ vectorstore.IDDeleter = (*Store)(nil)
)

type Store struct {
	engine pgstore.Store
}

func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	config.applyDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if err := initialize(ctx, config); err != nil {
		return nil, fmt.Errorf("pgvector.NewStore: initialize schema: %w", err)
	}

	engine, err := pgstore.New(ctx, pgstore.Config{
		Provider:       "pgvector",
		Pool:           config.Pool,
		SchemaName:     config.SchemaName,
		TableName:      config.TableName,
		MetadataColumn: config.MetadataColumn,
		Space:          config.Space,
		DistanceMetric: config.DistanceMetric,
	})
	if err != nil {
		return nil, fmt.Errorf("pgvector.NewStore: %w", err)
	}
	return &Store{engine: engine}, nil
}

func initialize(ctx context.Context, config StoreConfig) error {
	if !config.InitializeSchema {
		return nil
	}

	dimensions := config.Dimensions
	if dimensions <= 0 {
		return errors.New("dimensions must be > 0")
	}

	statements := []string{
		`CREATE EXTENSION IF NOT EXISTS vector`,
		fmt.Sprintf(`CREATE SCHEMA IF NOT EXISTS %s`, pgx.Identifier{config.SchemaName}.Sanitize()),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s.%s (
			id bytea PRIMARY KEY,
			content text NOT NULL,
			%s bytea NOT NULL,
			embedding vector(%d) NOT NULL,
CONSTRAINT %s CHECK (true)
		)`, pgx.Identifier{config.SchemaName}.Sanitize(), pgx.Identifier{config.TableName}.Sanitize(), pgx.Identifier{config.MetadataColumn}.Sanitize(), dimensions, pgx.Identifier{config.Space.StorageKey()}.Sanitize()),
	}
	if config.IndexType != IndexNone {
		statements = append(statements, fmt.Sprintf(
			`CREATE INDEX IF NOT EXISTS %s ON %s.%s USING %s (embedding %s)`,
			pgx.Identifier{config.IndexName}.Sanitize(), pgx.Identifier{config.SchemaName}.Sanitize(), pgx.Identifier{config.TableName}.Sanitize(), config.IndexType,
			config.DistanceMetric.IndexOpClass(),
		))
	}

	for _, statement := range statements {
		if _, err := config.Pool.Exec(ctx, statement); err != nil {
			firstLine, _, _ := strings.Cut(statement, "\n")
			return fmt.Errorf("execute %q: %w", firstLine, err)
		}
	}
	return nil
}

func (s *Store) CheckDocuments(ctx context.Context, space vectorstore.SpaceID, docs []*document.Document) error {
	return s.engine.CheckDocuments(ctx, space, docs)
}
func (s *Store) CheckQuery(ctx context.Context, space vectorstore.SpaceID, options vectorstore.SearchOptions) error {
	return s.engine.CheckQuery(ctx, space, options)
}
func (s *Store) Upsert(ctx context.Context, batch *vectorstore.Batch) error {
	return s.engine.Upsert(ctx, batch)
}
func (s *Store) Query(ctx context.Context, request *vectorstore.Query) (*vectorstore.SearchResponse, error) {
	return s.engine.Query(ctx, request)
}

func (s *Store) DeleteIDs(ctx context.Context, ids []string) error {
	return s.engine.DeleteIDs(ctx, ids)
}

func (s *Store) Scan(ctx context.Context, config postgres.ScanConfig) ([]*document.Document, error) {
	return s.engine.Scan(ctx, config)
}
