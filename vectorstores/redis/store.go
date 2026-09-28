package redis

import (
	"cmp"
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	goredis "github.com/redis/go-redis/v9"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/metadata"
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
const (
	redisSearchDialectVersion = 2
	filterPageSize            = 256
)

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

	store := &Store{
		client:            config.Client,
		indexName:         config.IndexName,
		keyPrefix:         config.KeyPrefix,
		contentField:      config.ContentField,
		embeddingField:    config.EmbeddingField,
		metadataJSONField: config.MetadataJSONField,
		metadataFields:    metadataFields,
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
// match this store's namespace and vector representation.
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
	raw, err := s.client.Do(ctx, "FT.INFO", s.indexName).Result()
	if err != nil {
		return fmt.Errorf("redis: FT.INFO %s: %w", s.indexName, err)
	}
	info, err := parseIndexInfo(raw)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrIncompatibleIndex, err)
	}
	if info.keyType != "HASH" || len(info.prefixes) != 1 || info.prefixes[0] != s.keyPrefix || info.filtered {
		return fmt.Errorf("%w: index %s must select exactly HASH keys with prefix %q and no FILTER", ErrIncompatibleIndex, s.indexName, s.keyPrefix)
	}
	for _, attribute := range info.attributes {
		if attribute.Attribute != s.embeddingField || attribute.Identifier != s.embeddingField {
			continue
		}
		if attribute.Type != "VECTOR" || attribute.DataType != "FLOAT32" || attribute.Dim <= 0 {
			return fmt.Errorf("%w: index %s must hold %s as a FLOAT32 vector", ErrIncompatibleIndex, s.indexName, s.embeddingField)
		}
		if !strings.EqualFold(attribute.DistanceMetric, string(s.distanceMetric)) {
			return fmt.Errorf("%w: index %s ranks %s by %s, but this store scores by %s", ErrIncompatibleIndex, s.indexName, s.embeddingField, attribute.DistanceMetric, s.distanceMetric)
		}
		if s.dimensions > 0 && attribute.Dim != s.dimensions {
			return fmt.Errorf("%w: index %s vector dimension is %d, want %d", ErrIncompatibleIndex, s.indexName, attribute.Dim, s.dimensions)
		}
		return nil
	}
	return fmt.Errorf("%w: index %s has no vector attribute %s", ErrIncompatibleIndex, s.indexName, s.embeddingField)
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
			Type:                   "FLOAT32",
			Dim:                    s.dimensions,
			DistanceMetric:         string(s.distanceMetric),
			MaxEdgesPerNode:        s.hnswM,
			MaxAllowedEdgesPerNode: s.hnswEFConstruct,
			EFRunTime:              s.hnswEFRuntime,
		}
	}
	return args
}

// DeleteWhere enumerates metadata, then deletes matching keys only while the
// observed metadata remains unchanged.
func (s *Store) DeleteWhere(ctx context.Context, expr filter.Predicate) (err error) {
	if expr == nil {
		return vectorstore.ErrMissingFilter
	}
	if err = expr.Validate(); err != nil {
		return fmt.Errorf("redis.Store.DeleteWhere: %w", err)
	}

	records, err := s.matchingRecords(ctx, expr)
	if err != nil {
		return err
	}
	for key, observed := range records {
		if _, err := s.client.Eval(ctx, deleteObservedMetadata, []string{key}, s.metadataJSONField, observed).Result(); err != nil {
			return fmt.Errorf("redis: delete %s: %w", key, err)
		}
	}
	return nil
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

const deleteObservedMetadata = `if redis.call("HGET", KEYS[1], ARGV[1]) == ARGV[2] then return redis.call("DEL", KEYS[1]) end return 0`

// matchingRecords scans the complete key namespace before a vector limit or any
// deletion. The JSON metadata record retains distinctions TAG and TEXT indexes
// lose, including scalar/array types, case, delimiters and whole-string LIKE.
func (s *Store) matchingRecords(ctx context.Context, expr filter.Predicate) (map[string]string, error) {
	if _, ring := s.client.(*goredis.Ring); ring {
		return nil, fmt.Errorf("redis: complete metadata enumeration for Ring clients: %w", errors.ErrUnsupported)
	}
	selected := make(map[string]string)
	var mu sync.Mutex
	seen := make(map[string]struct{})
	scan := func(ctx context.Context, client goredis.Cmdable) error {
		var cursor uint64
		pattern := strings.NewReplacer("\\", "\\\\", "*", "\\*", "?", "\\?", "[", "\\[", "]", "\\]").Replace(s.keyPrefix) + "*"
		for {
			keys, next, err := client.Scan(ctx, cursor, pattern, filterPageSize).Result()
			if err != nil {
				return fmt.Errorf("redis: scan document keys: %w", err)
			}
			for _, key := range keys {
				if !strings.HasPrefix(key, s.keyPrefix) || len(key) == len(s.keyPrefix) {
					return fmt.Errorf("redis: returned key %q is outside document namespace %q", key, s.keyPrefix)
				}
				mu.Lock()
				_, duplicate := seen[key]
				seen[key] = struct{}{}
				mu.Unlock()
				if duplicate {
					continue
				}
				raw, err := client.HGet(ctx, key, s.metadataJSONField).Result()
				if errors.Is(err, goredis.Nil) {
					exists, existsErr := client.Exists(ctx, key).Result()
					if existsErr != nil {
						return fmt.Errorf("redis: check vanished document %s: %w", key, existsErr)
					}
					if exists == 0 {
						continue
					}
					return fmt.Errorf("redis: document %s is missing metadata field %s", key, s.metadataJSONField)
				}
				if err != nil {
					return fmt.Errorf("redis: read metadata for %s: %w", key, err)
				}
				var values metadata.Map
				if decodeErr := jsonv2.Unmarshal([]byte(raw), &values); decodeErr != nil {
					return fmt.Errorf("redis: decode metadata for %s: %w", key, decodeErr)
				}
				decoded, err := values.Values()
				if err != nil {
					return err
				}
				matched, err := filter.Match(expr, decoded)
				if err != nil {
					return fmt.Errorf("redis: evaluate filter for %s: %w", key, err)
				}
				if matched {
					mu.Lock()
					selected[key] = raw
					mu.Unlock()
				}
			}
			if next == 0 {
				return nil
			}
			cursor = next
		}
	}
	if cluster, ok := s.client.(*goredis.ClusterClient); ok {
		if err := cluster.ForEachMaster(ctx, func(ctx context.Context, client *goredis.Client) error { return scan(ctx, client) }); err != nil {
			return nil, err
		}
	} else if err := scan(ctx, s.client); err != nil {
		return nil, err
	}
	return selected, nil
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
	for index, doc := range request.Documents {
		for _, field := range []string{s.contentField, s.embeddingField, s.metadataJSONField} {
			if _, exists := doc.Metadata[field]; exists {
				return fmt.Errorf("redis: documents[%d] metadata key %q is reserved", index, field)
			}
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
			metadataJSON, marshalErr := jsonv2.Marshal(doc.Metadata)
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
			arguments := make([]any, 0, len(fields)*2)
			for key, value := range fields {
				arguments = append(arguments, key, value)
			}
			pipe.Eval(ctx, replaceDocumentHash, []string{s.keyPrefix + id}, arguments...)
		}

		if _, err = pipe.Exec(ctx); err != nil {
			return fmt.Errorf("redis: replace document hashes: %w", err)
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

	var selected []string
	if req.Options.Filter != nil {
		records, matchErr := s.matchingRecords(ctx, req.Options.Filter)
		if matchErr != nil {
			return nil, matchErr
		}
		for key := range records {
			selected = append(selected, key)
		}
		slices.Sort(selected)
		if len(selected) == 0 {
			return &vectorstore.SearchResponse{}, nil
		}
	}

	vector, err := s.embeddingClient.EmbedText(ctx, req.Query)
	if err != nil {
		return nil, fmt.Errorf("redis: embed query: %w", err)
	}
	queryVec := float32sToBytes(embedding.Float32Vector(vector))

	queryStr := fmt.Sprintf("*=>[KNN %d @%s $%s AS %s]", req.Options.ResultLimit(), s.embeddingField, vectorParamName, distanceFieldName)

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

	groups := [][]string{nil}
	if req.Options.Filter != nil {
		groups = slices.Collect(slices.Chunk(selected, filterPageSize))
	}
	for _, group := range groups {
		opts.InKeys = make([]any, len(group))
		for index, key := range group {
			opts.InKeys[index] = key
		}
		result, err := s.client.FTSearchWithArgs(ctx, s.indexName, queryStr, opts).Result()
		if err != nil {
			return nil, fmt.Errorf("redis: FT.SEARCH %s: %w", s.indexName, err)
		}
		if err := checkSearchCompleteness(s.indexName, result); err != nil {
			return nil, err
		}
		for _, hit := range result.Docs {
			doc, err := s.toDocument(hit)
			if err != nil {
				return nil, err
			}
			if req.Options.Filter != nil {
				if !slices.Contains(group, hit.ID) {
					return nil, fmt.Errorf("redis: search returned unselected key %q", hit.ID)
				}
				values, decodeErr := doc.Metadata.Values()
				if decodeErr != nil {
					return nil, decodeErr
				}
				matched, matchErr := filter.Match(req.Options.Filter, values)
				if matchErr != nil {
					return nil, fmt.Errorf("redis: validate returned metadata for %s: %w", hit.ID, matchErr)
				}
				if !matched {
					return nil, fmt.Errorf("redis: metadata for %s changed after filter selection", hit.ID)
				}
			}
			score, err := s.scoreFromFields(hit.Fields)
			if err != nil {
				return nil, err
			}
			if score < req.Options.MinScore {
				continue
			}
			docs = append(docs, &vectorstore.SearchResult{Document: doc, Score: score})
		}
	}
	slices.SortFunc(docs, func(left, right *vectorstore.SearchResult) int { return cmp.Compare(right.Score, left.Score) })
	if len(docs) > req.Options.ResultLimit() {
		docs = docs[:req.Options.ResultLimit()]
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
	if !strings.HasPrefix(hit.ID, s.keyPrefix) {
		return nil, fmt.Errorf("redis: returned key %q is outside document namespace %q", hit.ID, s.keyPrefix)
	}
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
		if err := jsonv2.Unmarshal([]byte(raw), &doc.Metadata); err != nil {
			return nil, fmt.Errorf("redis: decode metadata for %q: %w", id, err)
		}
	}
	return doc, nil
}

// The hash is one owned record. Install the complete new projection before
// pruning old fields, so a malformed HSET cannot destroy the previous value.
const replaceDocumentHash = `
local previous = redis.call('HKEYS', KEYS[1])
redis.call('HSET', KEYS[1], unpack(ARGV))
local current = {}
for i = 1, #ARGV, 2 do current[ARGV[i]] = true end
for _, field in ipairs(previous) do
    if not current[field] then redis.call('HDEL', KEYS[1], field) end
end
return 1
`
