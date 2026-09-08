package redis

import (
	"context"
	"errors"
	"fmt"
	"slices"

	goredis "github.com/redis/go-redis/v9"

	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/vectorstore"
)

var (
	_ vectorstore.Indexer       = (*Store)(nil)
	_ vectorstore.Searcher      = (*Store)(nil)
	_ vectorstore.FilterDeleter = (*Store)(nil)
	_ vectorstore.IDDeleter     = (*Store)(nil)
)

// RediSearch requires dialect 2 for the vector-query syntax used by Store.
const redisSearchDialectVersion = 2

// Store is a Redis-backed implementation of the vectorstore capability interfaces. It
// stores documents as Redis HASHes and queries them through RediSearch
// vector + metadata indexes.
type Store struct {
	client            goredis.UniversalClient
	indexName         string
	keyPrefix         string
	contentField      string
	embeddingField    string
	metadataJSONField string
	metadataFields    []MetadataField
	fieldTypes        map[string]MetadataFieldType
	embeddingClient   embeddingclient.Client
	documentBatcher   vectorstore.Batcher
	dimensions        int
	distanceMetric    DistanceMetric
	indexAlgorithm    IndexAlgorithm
	hnswM             int
	hnswEFConstruct   int
	hnswEFRuntime     int
}

// NewStore performs schema setup during construction, which is why it takes
// a context: a store returned before its search index exists would fail on
// the first index rather than at wiring, where the misconfiguration actually
// is.
func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	config.applyDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}

	embeddingClient, err := embeddingclient.New(config.EmbeddingModel)
	if err != nil {
		return nil, fmt.Errorf("redis: create embedding client: %w", err)
	}

	metadataFields := slices.Clone(config.MetadataFields)
	fieldTypes := make(map[string]MetadataFieldType, len(metadataFields))
	for _, f := range metadataFields {
		if f.Name == "" {
			return nil, errors.New("redis: MetadataField.Name must not be empty")
		}
		fieldTypes[f.Name] = f.Type
	}

	store := &Store{
		client:            config.Client,
		indexName:         config.IndexName,
		keyPrefix:         config.KeyPrefix,
		contentField:      config.ContentField,
		embeddingField:    config.EmbeddingField,
		metadataJSONField: config.MetadataJSONField,
		metadataFields:    metadataFields,
		fieldTypes:        fieldTypes,
		embeddingClient:   embeddingClient,
		documentBatcher:   config.DocumentBatcher,
		dimensions:        config.Dimensions,
		distanceMetric:    config.DistanceMetric,
		indexAlgorithm:    config.IndexAlgorithm,
		hnswM:             config.HNSWM,
		hnswEFConstruct:   config.HNSWEFConstruct,
		hnswEFRuntime:     config.HNSWEFRuntime,
	}

	if err = store.initialize(ctx, config.InitializeSchema); err != nil {
		return nil, fmt.Errorf("redis: initialize store: %w", err)
	}
	return store, nil
}

// initialize resolves the vector dimensionality and creates the
// RediSearch index when requested.
func (s *Store) initialize(ctx context.Context, initSchema bool) error {
	if !initSchema {
		return nil
	}
	if s.dimensions <= 0 {
		return errors.New("redis: Dimensions must be > 0")
	}

	// FT._LIST returns existing index names — skip creation when ours
	// is already there.
	existing, err := s.client.FT_List(ctx).Result()
	if err != nil {
		return fmt.Errorf("FT._LIST: %w", err)
	}
	if slices.Contains(existing, s.indexName) {
		return nil
	}

	schema, err := s.buildSchema()
	if err != nil {
		return err
	}
	opts := &goredis.FTCreateOptions{
		OnHash: true,
		Prefix: []any{s.keyPrefix},
	}
	if _, err = s.client.FTCreate(ctx, s.indexName, opts, schema...).Result(); err != nil {
		return fmt.Errorf("FT.CREATE %s: %w", s.indexName, err)
	}
	return nil
}

func (s *Store) buildSchema() ([]*goredis.FieldSchema, error) {
	schema := []*goredis.FieldSchema{
		{
			FieldName: s.contentField,
			FieldType: goredis.SearchFieldTypeText,
			Weight:    1.0,
		},
		{
			FieldName:  s.embeddingField,
			FieldType:  goredis.SearchFieldTypeVector,
			VectorArgs: s.vectorArgs(),
		},
	}

	for _, f := range s.metadataFields {
		fieldType, ok := f.Type.searchFieldType()
		if !ok {
			return nil, fmt.Errorf("redis: metadata field %q has unsupported Type %q", f.Name, f.Type)
		}
		fs := &goredis.FieldSchema{
			FieldName: f.Name,
			FieldType: fieldType,
			Sortable:  f.Sortable,
		}
		schema = append(schema, fs)
	}
	return schema, nil
}

func (s *Store) vectorArgs() *goredis.FTVectorArgs {
	args := &goredis.FTVectorArgs{}
	switch s.indexAlgorithm {
	case AlgorithmFlat:
		args.FlatOptions = &goredis.FTFlatOptions{
			Type:           "FLOAT32",
			Dim:            s.dimensions,
			DistanceMetric: string(s.distanceMetric),
		}
	case AlgorithmHNSW:
		fallthrough
	default:
		args.HNSWOptions = &goredis.FTHNSWOptions{
			Type:            "FLOAT32",
			Dim:             s.dimensions,
			DistanceMetric:  string(s.distanceMetric),
			MaxEdgesPerNode: s.hnswM,
			EFRunTime:       s.hnswEFRuntime,
		}
	}
	return args
}

func (s *Store) Close() error { return nil }
