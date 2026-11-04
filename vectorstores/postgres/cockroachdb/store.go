package cockroachdb

import (
	"cmp"
	"context"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/vectorstores/postgres"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/vectorstores/postgres/internal/pgstore"
)

const Provider = "CockroachDB"

const (
	DefaultSchemaName     = "public"
	DefaultTableName      = "vector_store"
	DefaultMetadataColumn = "metadata"
	DefaultIndexSuffix    = "_embedding_idx"
)

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
	InitializeSchema bool
}

func (s *StoreConfig) applyDefaults() {
	s.SchemaName = cmp.Or(s.SchemaName, DefaultSchemaName)
	s.TableName = cmp.Or(s.TableName, DefaultTableName)
	s.MetadataColumn = cmp.Or(s.MetadataColumn, DefaultMetadataColumn)
	if s.IndexName == "" {
		s.IndexName = s.TableName + DefaultIndexSuffix
	}
	s.DistanceMetric = cmp.Or(s.DistanceMetric, postgres.DistanceCosine)
}

func (s StoreConfig) Validate() error {
	s.applyDefaults()
	if s.Pool == nil {
		return errors.New("cockroachdb: Pool is required")
	}
	if err := s.Space.Validate(); err != nil {
		return err
	}

	if s.Dimensions < 0 {
		return errors.New("cockroachdb: Dimensions must be >= 0")
	}
	if !s.DistanceMetric.Valid() {
		return fmt.Errorf("cockroachdb: unsupported DistanceMetric %q", s.DistanceMetric)
	}
	return s.validateIdentifiers()
}

func (s StoreConfig) validateIdentifiers() error {
	switch s.MetadataColumn {
	case "id", "content", "embedding":
		return errors.New("cockroachdb: MetadataColumn conflicts with a document column")
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
		return nil, fmt.Errorf("cockroachdb.NewStore: initialize schema: %w", err)
	}

	engine, err := pgstore.New(ctx, pgstore.Config{
		Provider:       "cockroachdb",
		Pool:           config.Pool,
		SchemaName:     config.SchemaName,
		TableName:      config.TableName,
		MetadataColumn: config.MetadataColumn,
		Space:          config.Space,
		DistanceMetric: config.DistanceMetric,
	})
	if err != nil {
		return nil, fmt.Errorf("cockroachdb.NewStore: %w", err)
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

	if _, err := config.Pool.Exec(ctx, fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", pgx.Identifier{config.SchemaName}.Sanitize())); err != nil {
		return fmt.Errorf("create schema %s: %w", config.SchemaName, err)
	}
	fullTable := pgx.Identifier{config.SchemaName, config.TableName}.Sanitize()
	statement := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		id BYTES PRIMARY KEY,
		content STRING NOT NULL,
		%s BYTES NOT NULL,
		embedding VECTOR(%d) NOT NULL,
		CONSTRAINT %s CHECK (true),
 VECTOR INDEX %s (embedding %s)
	)`, fullTable, pgx.Identifier{config.MetadataColumn}.Sanitize(), dimensions, pgx.Identifier{config.Space.StorageKey()}.Sanitize(), pgx.Identifier{config.IndexName}.Sanitize(), config.DistanceMetric.IndexOpClass())
	if _, err := config.Pool.Exec(ctx, statement); err != nil {
		return fmt.Errorf("create table %s: %w", fullTable, err)
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

func (s *Store) Scan(ctx context.Context, request vectorstore.ScanRequest) ([]*document.Document, error) {
	return s.engine.Scan(ctx, request)
}
