package redis

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

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

// initialize confirms the index agrees with this store's configuration, and
// creates it when initSchema permits.
//
// The check is not conditional on initSchema. That flag answers "may I create
// a missing index", which is a different question from "is the index I found
// the one I was configured for" — and the second question matters most for an
// index provisioned out of band, which is exactly the case the flag turns off.
// Skipping it there left the configured metric unverified, and a wrong metric
// returns scores that are wrong rather than absent.
func (s *Store) initialize(ctx context.Context, initSchema bool) error {
	// FT._LIST returns existing index names.
	existing, err := s.client.FT_List(ctx).Result()
	if err != nil {
		return fmt.Errorf("FT._LIST: %w", err)
	}
	if slices.Contains(existing, s.indexName) {
		return s.checkExistingIndex(ctx)
	}
	if !initSchema {
		return fmt.Errorf("%w: index %s does not exist and InitializeSchema is disabled",
			ErrIncompatibleIndex, s.indexName)
	}
	if s.dimensions <= 0 {
		return errors.New("redis: Dimensions must be > 0")
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

// ErrIncompatibleIndex reports an existing index whose vector field does not
// match the configuration this store scores against.
var ErrIncompatibleIndex = errors.New("redis: existing index is incompatible")

// checkExistingIndex verifies that an index this store did not create agrees
// with the configuration it scores against.
//
// Existence is not agreement. Search converts RediSearch's distance into a
// Score using the metric from this store's own config, so an index built with
// L2 while the config says COSINE returns scores that are wrong rather than
// missing: nothing fails, the ranking is silently mis-scaled. Skipping creation
// because the name was taken accepted exactly that.
//
// The dimension is compared only when the caller declared one. Dimensions are
// required to create an index and optional to attach to one, so demanding a
// value here would make an out-of-band index unusable without repeating a fact
// the index already holds. A wrong width fails on the first write regardless.
func (s *Store) checkExistingIndex(ctx context.Context) error {
	info, err := s.client.FTInfo(ctx, s.indexName).Result()
	if err != nil {
		return fmt.Errorf("redis: FT.INFO %s: %w", s.indexName, err)
	}
	for _, attribute := range info.Attributes {
		if attribute.Attribute != s.embeddingField && attribute.Identifier != s.embeddingField {
			continue
		}
		if !strings.EqualFold(attribute.DistanceMetric, string(s.distanceMetric)) {
			return fmt.Errorf("%w: index %s ranks %s by %s, but this store scores by %s",
				ErrIncompatibleIndex, s.indexName, s.embeddingField,
				attribute.DistanceMetric, s.distanceMetric)
		}
		if s.dimensions > 0 && attribute.Dim != s.dimensions {
			return fmt.Errorf("%w: index %s holds %s with %d dimensions, but this store is configured for %d",
				ErrIncompatibleIndex, s.indexName, s.embeddingField, attribute.Dim, s.dimensions)
		}
		return nil
	}
	return fmt.Errorf("%w: index %s has no vector attribute %s",
		ErrIncompatibleIndex, s.indexName, s.embeddingField)
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
