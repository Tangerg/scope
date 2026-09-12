package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	goredis "github.com/redis/go-redis/v9"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
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

// Delete looks up documents matching the filter via FT.SEARCH, then
// removes the underlying keys with DEL.
func (s *Store) DeleteWhere(ctx context.Context, expr filter.Predicate) (err error) {
	if expr == nil {
		return vectorstore.ErrMissingFilter
	}
	if err = expr.Validate(); err != nil {
		return fmt.Errorf("redis.Store.DeleteWhere: %w", err)
	}

	var query string
	query, err = s.buildFilterQuery(expr)
	if err != nil {
		return err
	}
	if query == "*" {
		return errors.New("redis: refusing to DELETE on empty filter — pass a non-trivial expression")
	}

	const pageSize = 500
	opts := &goredis.FTSearchOptions{
		NoContent:      true,
		LimitOffset:    0,
		Limit:          pageSize,
		DialectVersion: redisSearchDialectVersion,
	}
	// Only an empty page establishes that nothing matches. RediSearch runs
	// FT.SEARCH under a query timeout whose default ON_TIMEOUT policy returns
	// the hits accumulated so far, so a page shorter than the limit can mean a
	// truncated scan rather than an exhausted match set. Re-querying after each
	// DEL converges in either case; trusting a short page would report success
	// while matching documents remained.
	for {
		result, err := s.client.FTSearchWithArgs(ctx, s.indexName, query, opts).Result()
		if err != nil {
			return fmt.Errorf("redis: FT.SEARCH %s: %w", s.indexName, err)
		}
		if completenessErr := checkSearchCompleteness(s.indexName, result); completenessErr != nil {
			return completenessErr
		}
		if len(result.Docs) == 0 {
			return nil
		}
		keys := make([]string, 0, len(result.Docs))
		for _, hit := range result.Docs {
			keys = append(keys, hit.ID)
		}
		if _, err = s.client.Del(ctx, keys...).Result(); err != nil {
			return fmt.Errorf("redis: DEL: %w", err)
		}
	}
}

// DeleteIDs removes documents by id, resolving each to its HASH key
// `<KeyPrefix><id>` and issuing a single DEL. An empty slice is a
// no-op; unknown ids are silently ignored (idempotent). Implements
// [vectorstore.IDDeleter].
func (s *Store) DeleteIDs(ctx context.Context, ids []string) (err error) {
	if len(ids) == 0 {
		return nil
	}

	keys := make([]string, len(ids))
	for i, id := range ids {
		keys[i] = s.keyPrefix + id
	}
	if _, err = s.client.Del(ctx, keys...).Result(); err != nil {
		return fmt.Errorf("redis: DEL: %w", err)
	}
	return nil
}

// buildFilterQuery turns the optional filter predicate into a
// RediSearch query string. Returns "*" (match-all) when filter is nil,
// matching the syntax FT.SEARCH expects in front of the KNN tail.
func (s *Store) buildFilterQuery(expr filter.Predicate) (string, error) {
	if expr == nil {
		return "*", nil
	}
	v := newVisitor(s.fieldTypes)
	if err := expr.Accept(v); err != nil {
		return "", fmt.Errorf("redis: convert filter: %w", err)
	}
	fragment := v.snapshot()
	if fragment == "" {
		return "*", nil
	}
	return "(" + fragment + ")", nil
}

// Index embeds documents and writes them as Redis HASHes keyed by
// `<KeyPrefix><id>`.
func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) (err error) {
	if validateErr := request.Validate(); validateErr != nil {
		return fmt.Errorf("redis.Store.Index: %w", validateErr)
	}
	for index, doc := range request.Documents {
		if doc.Media != nil {
			return fmt.Errorf("redis.Store.Index: %w: documents[%d] contains unsupported media", vectorstore.ErrInvalidDocument, index)
		}
	}

	var batches []*vectorstore.IndexRequest
	batches, err = request.Batch(ctx, s.documentBatcher)
	if err != nil {
		return fmt.Errorf("redis: batch documents: %w", err)
	}

	for _, batch := range batches {
		docs := batch.Documents
		texts, err := batch.Texts()
		if err != nil {
			return fmt.Errorf("vectorstore: project document text: %w", err)
		}
		vectors, err := s.embeddingClient.EmbedTexts(ctx, texts)
		if err != nil {
			return fmt.Errorf("redis: embed documents: %w", err)
		}

		pipe := s.client.Pipeline()
		for i, doc := range docs {
			id := doc.ID
			metadataValues, valuesErr := doc.Metadata.Values()
			if valuesErr != nil {
				return fmt.Errorf("redis: decode metadata for %s: %w", id, valuesErr)
			}
			metadataJSON, marshalErr := json.Marshal(doc.Metadata)
			if marshalErr != nil {
				return fmt.Errorf("redis: encode metadata for %s: %w", id, marshalErr)
			}
			fields := map[string]any{
				s.contentField:      doc.Text,
				s.embeddingField:    float32sToBytes(embedding.Float32Vector(vectors[i])),
				s.metadataJSONField: string(metadataJSON),
			}
			// The declared fields are the index projection: RediSearch indexes a
			// HASH field's text as its declared type, so each one has to hold the
			// value in the form the index expects. The JSON field above is the
			// record a search reads back, which is why that projection no longer
			// has to be reversible.
			for k, v := range metadataValues {
				field, formatErr := formatMetadataValue(v)
				if formatErr != nil {
					return fmt.Errorf("%w (document %s, key %s)", formatErr, id, k)
				}
				fields[k] = field
			}
			pipe.HSet(ctx, s.keyPrefix+id, fields)
		}

		if _, err = pipe.Exec(ctx); err != nil {
			return fmt.Errorf("redis: pipeline HSET: %w", err)
		}
	}
	return nil
}

// Search embeds the query, runs a KNN search through RediSearch,
// and returns the matching documents above MinScore.
func (s *Store) Search(ctx context.Context, req *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	var docs []*vectorstore.SearchResult
	if err = req.Validate(); err != nil {
		return nil, fmt.Errorf("redis.Store.Search: %w", err)
	}
	if err = req.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, fmt.Errorf("redis.Store.Search: %w", err)
	}

	defer func() {
		if err == nil {
			err = response.ValidateFor(req)
		}
	}()

	vector, err := s.embeddingClient.EmbedText(ctx, req.Query)
	if err != nil {
		return nil, fmt.Errorf("redis: embed query: %w", err)
	}
	queryVec := float32sToBytes(embedding.Float32Vector(vector))

	filterQuery, err := s.buildFilterQuery(req.Options.Filter)
	if err != nil {
		return nil, err
	}

	// RediSearch hybrid syntax: <filter>=>[KNN <k> @embedding $vec AS distance]
	queryStr := fmt.Sprintf(
		"%s=>[KNN %d @%s $%s AS %s]",
		filterQuery, req.Options.ResultLimit(), s.embeddingField, vectorParamName, distanceFieldName,
	)

	opts := &goredis.FTSearchOptions{
		Params: map[string]any{
			vectorParamName: queryVec,
		},
		Return:         s.returnFields(),
		LimitOffset:    0,
		Limit:          req.Options.ResultLimit(),
		DialectVersion: redisSearchDialectVersion,
		SortBy: []goredis.FTSearchSortBy{
			{FieldName: distanceFieldName, Asc: true},
		},
	}

	result, err := s.client.FTSearchWithArgs(ctx, s.indexName, queryStr, opts).Result()
	if err != nil {
		return nil, fmt.Errorf("redis: FT.SEARCH %s: %w", s.indexName, err)
	}
	if err := checkSearchCompleteness(s.indexName, result); err != nil {
		return nil, err
	}

	docs = make([]*vectorstore.SearchResult, 0, len(result.Docs))
	for _, hit := range result.Docs {
		score, err := s.scoreFromFields(hit.Fields)
		if err != nil {
			return nil, err
		}
		if score < req.Options.MinScore {
			continue
		}
		doc, err := s.toDocument(hit)
		if err != nil {
			return nil, err
		}
		docs = append(docs, &vectorstore.SearchResult{Document: doc, Score: score})
	}
	return &vectorstore.SearchResponse{Results: docs}, nil
}

// returnFields is everything a search result is read from. RETURN limits the
// reply to the fields it lists, so a field missing here reads back as an absent
// field rather than as an error — which is why the list is named and pinned
// rather than assembled at the call site.
//
// The declared metadata fields are absent on purpose: they exist so RediSearch
// can index and filter on them, and the metadata field is what a result reads
// its metadata back from, so the projection never has to come over the wire.
func (s *Store) returnFields() []goredis.FTSearchReturn {
	return []goredis.FTSearchReturn{
		{FieldName: s.contentField},
		{FieldName: distanceFieldName},
		{FieldName: s.metadataJSONField},
	}
}

func (s *Store) scoreFromFields(fields map[string]string) (vectorstore.Score, error) {
	raw, ok := fields[distanceFieldName]
	if !ok {
		return 0, fmt.Errorf("redis: missing distance field %q in result", distanceFieldName)
	}
	dist, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("redis: parse distance %q: %w", raw, err)
	}
	return s.distanceMetric.score(dist), nil
}

func (s *Store) toDocument(hit goredis.Document) (*document.Document, error) {
	id := strings.TrimPrefix(hit.ID, s.keyPrefix)
	if id == "" {
		return nil, errors.New("redis: search result is missing document ID")
	}
	text := hit.Fields[s.contentField]
	if text == "" {
		return nil, fmt.Errorf("redis: document %q is missing field %q", id, s.contentField)
	}
	doc := &document.Document{
		ID:   id,
		Text: text,
	}

	// Metadata comes from the JSON field rather than from the declared index
	// fields. A declared field holds the value in the form its RediSearch type
	// expects, so reading it back turned a number into a float64 and everything
	// else into a string, and an undeclared key had no field to read at all —
	// a search returned a document that differed from the one that was written.
	if raw, ok := hit.Fields[s.metadataJSONField]; ok && raw != "" {
		if err := json.Unmarshal([]byte(raw), &doc.Metadata); err != nil {
			return nil, fmt.Errorf("redis: decode metadata for %q: %w", id, err)
		}
	}
	return doc, nil
}
