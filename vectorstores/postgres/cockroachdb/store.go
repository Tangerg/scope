package cockroachdb

import (
	"cmp"
	"context"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/vectorstores/postgres"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
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
	Pool            *pgxpool.Pool
	SchemaName      string
	TableName       string
	IndexName       string
	MetadataColumn  string
	EmbeddingModel  embedding.Model
	DocumentBatcher vectorstore.Batcher
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
	if lo.IsNil(s.EmbeddingModel) {
		return errors.New("cockroachdb: EmbeddingModel is required")
	}
	if lo.IsNil(s.DocumentBatcher) {
		return errors.New("cockroachdb: DocumentBatcher is required")
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
	_ vectorstore.Indexer       = (*Store)(nil)
	_ vectorstore.Searcher      = (*Store)(nil)
	_ vectorstore.FilterDeleter = (*Store)(nil)
	_ vectorstore.IDDeleter     = (*Store)(nil)
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
		Provider:        "cockroachdb",
		Pool:            config.Pool,
		SchemaName:      config.SchemaName,
		TableName:       config.TableName,
		MetadataColumn:  config.MetadataColumn,
		EmbeddingModel:  config.EmbeddingModel,
		DocumentBatcher: config.DocumentBatcher,
		DistanceMetric:  config.DistanceMetric,
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
		VECTOR INDEX %s (embedding %s)
	)`, fullTable, pgx.Identifier{config.MetadataColumn}.Sanitize(), dimensions, pgx.Identifier{config.IndexName}.Sanitize(), config.DistanceMetric.IndexOpClass())
	if _, err := config.Pool.Exec(ctx, statement); err != nil {
		return fmt.Errorf("create table %s: %w", fullTable, err)
	}
	return nil
}

func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) error {
	return s.engine.Index(ctx, request)
}

func (s *Store) Search(ctx context.Context, request *vectorstore.SearchRequest) (*vectorstore.SearchResponse, error) {
	return s.engine.Search(ctx, request)
}

func (s *Store) DeleteWhere(ctx context.Context, predicate filter.Predicate) error {
	return s.engine.DeleteWhere(ctx, predicate)
}

func (s *Store) DeleteIDs(ctx context.Context, ids []string) error {
	return s.engine.DeleteIDs(ctx, ids)
}
