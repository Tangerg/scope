package cockroachdb

import (
	"cmp"
	"context"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/vectorstores/postgres"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Tangerg/scope/core/vectorstore"
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
	Space          vectorstore.SpaceID
	DistanceMetric postgres.DistanceMetric

	// CreateDimensions provisions the schema and table with this vector width
	// when they do not exist; zero opens an existing table. The native column
	// owns the width, so an existing table must already declare it.
	CreateDimensions int
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

	if s.CreateDimensions < 0 {
		return errors.New("cockroachdb: CreateDimensions must not be negative")
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

func NewStore(ctx context.Context, config StoreConfig) (*postgres.Store, error) {
	config.applyDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if config.CreateDimensions > 0 {
		if err := create(ctx, config); err != nil {
			return nil, fmt.Errorf("cockroachdb.NewStore: create schema: %w", err)
		}
	}

	store, err := postgres.NewStore(ctx, postgres.StoreConfig{
		Pool:               config.Pool,
		SchemaName:         config.SchemaName,
		TableName:          config.TableName,
		MetadataColumn:     config.MetadataColumn,
		Space:              config.Space,
		DistanceMetric:     config.DistanceMetric,
		ExpectedDimensions: config.CreateDimensions,
	})
	if err != nil {
		return nil, fmt.Errorf("cockroachdb.NewStore: %w", err)
	}
	return store, nil
}

func create(ctx context.Context, config StoreConfig) error {
	dimensions := config.CreateDimensions
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
