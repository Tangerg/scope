package redis

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"

	goredis "github.com/redis/go-redis/v9"

	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

const (
	redisSearchDialectVersion = 2
	filterPageSize            = 256
)

var ErrIncompatibleIndex = errors.New("redis: native index is incompatible")
var (
	_ vectorstore.Indexer       = (*Store)(nil)
	_ vectorstore.Searcher      = (*Store)(nil)
	_ vectorstore.IDDeleter     = (*Store)(nil)
	_ vectorstore.FilterDeleter = (*Store)(nil)
)

type Store struct {
	client          RedisClient
	indexName       string
	schema          nativeSchema
	embeddingClient embeddingclient.Client
	documentBatcher vectorstore.Batcher
}

func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	embeddingClient, err := embeddingclient.New(config.EmbeddingModel)
	if err != nil {
		return nil, err
	}
	store := &Store{client: config.Client, indexName: cmp.Or(config.IndexName, DefaultIndexName), embeddingClient: embeddingClient, documentBatcher: config.DocumentBatcher}
	raw, err := store.client.Do(ctx, "FT.INFO", store.indexName).Result()
	if err != nil {
		return nil, err
	}
	info, err := parseIndexInfo(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrIncompatibleIndex, err)
	}
	if info.name != store.indexName || info.keyType != "HASH" || len(info.prefixes) != 1 || info.prefixes[0] == "" || info.filtered {
		return nil, fmt.Errorf("%w: requires one concrete HASH namespace without FILTER", ErrIncompatibleIndex)
	}
	var vectorFound bool
	for _, attribute := range info.attributes {
		if attribute.Identifier != attribute.Attribute {
			return nil, fmt.Errorf("%w: native aliases are unsupported", ErrIncompatibleIndex)
		}
		if attribute.Attribute == contentField && attribute.Type == "TEXT" {
			continue
		}
		if vectorFound || attribute.Attribute != embeddingField || attribute.Type != "VECTOR" || attribute.DataType != "FLOAT32" || attribute.Dim <= 0 {
			return nil, fmt.Errorf("%w: requires one FLOAT32 embedding field", ErrIncompatibleIndex)
		}
		switch attribute.DistanceMetric {
		case "COSINE", "L2", "IP":
		default:
			return nil, fmt.Errorf("%w: unsupported distance %q", ErrIncompatibleIndex, attribute.DistanceMetric)
		}
		store.schema = nativeSchema{prefix: info.prefixes[0], dimensions: attribute.Dim, metric: attribute.DistanceMetric}
		vectorFound = true
	}
	if !vectorFound {
		return nil, fmt.Errorf("%w: embedding field is missing", ErrIncompatibleIndex)
	}
	if _, err = store.matchingRecords(ctx, nil); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) matchingRecords(ctx context.Context, predicate filter.Predicate) (map[string]string, error) {
	selected := make(map[string]string)
	seen := make(map[string]struct{})
	var mu sync.Mutex
	scan := func(ctx context.Context, client hashScanner) error {
		var cursor uint64
		pattern := strings.NewReplacer("\\", "\\\\", "*", "\\*", "?", "\\?", "[", "\\[", "]", "\\]").Replace(s.schema.prefix) + "*"
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			keys, next, err := client.Scan(ctx, cursor, pattern, filterPageSize).Result()
			if err != nil {
				return err
			}
			for _, key := range keys {
				if !strings.HasPrefix(key, s.schema.prefix) || len(key) == len(s.schema.prefix) {
					return errors.New("redis: SCAN returned a key outside the document namespace")
				}
				mu.Lock()
				_, duplicate := seen[key]
				seen[key] = struct{}{}
				mu.Unlock()
				if duplicate {
					continue
				}
				fields, err := client.HGetAll(ctx, key).Result()
				if err != nil {
					return err
				}
				if len(fields) == 0 {
					continue
				}
				doc, err := s.schema.decode(key, fields)
				if err != nil {
					return err
				}
				if predicate != nil {
					values, err := doc.Metadata.Values()
					if err != nil {
						return err
					}
					matched, err := filter.Match(predicate, values)
					if err != nil {
						return err
					}
					if !matched {
						continue
					}
				}
				mu.Lock()
				selected[key] = fields[metadataField]
				mu.Unlock()
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

func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	records := make(map[string]hashRecord, len(request.Documents))
	for _, doc := range request.Documents {
		if doc.Media != nil {
			return vectorstore.ErrInvalidDocument
		}
		facts, err := doc.Metadata.MarshalJSON()
		if err != nil {
			return err
		}
		records[doc.ID] = hashRecord{key: s.schema.prefix + doc.ID, content: doc.Text, metadata: string(facts)}
	}
	batches, err := request.Batch(ctx, s.documentBatcher)
	if err != nil {
		return err
	}
	var prepared [][]hashRecord
	for _, batch := range batches {
		texts, err := batch.Texts()
		if err != nil {
			return err
		}
		vectors, err := s.embeddingClient.EmbedTexts(ctx, texts)
		if err != nil {
			return err
		}
		rows := make([]hashRecord, len(batch.Documents))
		for i, doc := range batch.Documents {
			vector, err := s.schema.vector(vectors[i])
			if err != nil {
				return err
			}
			rows[i] = records[doc.ID]
			rows[i].vector = vector
		}
		prepared = append(prepared, rows)
	}
	for _, rows := range prepared {
		pipe := s.client.Pipeline()
		for _, row := range rows {
			pipe.Eval(ctx, replaceDocumentHash, []string{row.key}, contentField, row.content, embeddingField, row.vector, metadataField, row.metadata)
		}
		commands, err := pipe.Exec(ctx)
		if err != nil {
			return err
		}
		if len(commands) != len(rows) {
			return errors.New("redis: HASH writes did not acknowledge every record")
		}
		for _, command := range commands {
			ack, ok := command.(*goredis.Cmd)
			if !ok {
				return errors.New("redis: HASH replacement returned an invalid acknowledgment")
			}
			value, err := ack.Int64()
			if err != nil {
				return err
			}
			if value != 1 {
				return errors.New("redis: HASH replacement was not acknowledged")
			}
		}
	}
	return nil
}

func (s *Store) Search(ctx context.Context, request *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	if err = request.Validate(); err != nil {
		return nil, err
	}
	if err = request.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, err
	}
	defer func() {
		if err == nil {
			err = response.ValidateFor(request)
		}
		if err != nil {
			response = nil
		}
	}()
	records, err := s.matchingRecords(ctx, request.Options.Filter)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return &vectorstore.SearchResponse{Results: []*vectorstore.SearchResult{}}, nil
	}
	vector, err := s.embeddingClient.EmbedText(ctx, request.Query)
	if err != nil {
		return nil, err
	}
	query, err := s.schema.vector(vector)
	if err != nil {
		return nil, err
	}
	groups := [][]string{nil}
	if request.Options.Filter != nil {
		groups = slices.Collect(slices.Chunk(slices.Sorted(maps.Keys(records)), filterPageSize))
	}
	var ranked []rankedHit
	seen := make(map[string]struct{})
	for _, group := range groups {
		args := []any{"FT.SEARCH", s.indexName, fmt.Sprintf("*=>[KNN %d @%s $%s AS %s]", request.Options.ResultLimit(), embeddingField, vectorParamName, distanceFieldName)}
		if len(group) > 0 {
			args = append(args, "INKEYS", len(group))
			for _, key := range group {
				args = append(args, key)
			}
		}
		args = append(args, "RETURN", 4, contentField, embeddingField, metadataField, distanceFieldName, "SORTBY", distanceFieldName, "ASC", "LIMIT", 0, request.Options.ResultLimit(), "PARAMS", 2, vectorParamName, query, "DIALECT", redisSearchDialectVersion)
		raw, err := s.client.Do(ctx, args...).Result()
		if err != nil {
			return nil, err
		}
		result, err := parseSearchResult(raw)
		if err != nil {
			return nil, err
		}
		if len(result.Docs) != min(result.Total, request.Options.ResultLimit()) {
			return nil, errors.New("redis: native result count does not acknowledge complete hits")
		}
		for _, hit := range result.Docs {
			if _, duplicate := seen[hit.ID]; duplicate {
				return nil, errors.New("redis: native search repeated a document key")
			}
			seen[hit.ID] = struct{}{}
			fields := maps.Clone(hit.Fields)
			raw, ok := fields[distanceFieldName]
			if !ok {
				return nil, errors.New("redis: native hit is missing its distance")
			}
			delete(fields, distanceFieldName)
			doc, err := s.schema.decode(hit.ID, fields)
			if err != nil {
				return nil, err
			}
			if request.Options.Filter != nil {
				if !slices.Contains(group, hit.ID) {
					return nil, errors.New("redis: native hit is outside Core membership")
				}
				values, decodeErr := doc.Metadata.Values()
				if decodeErr != nil {
					return nil, decodeErr
				}
				matched, matchErr := filter.Match(request.Options.Filter, values)
				if matchErr != nil {
					return nil, matchErr
				}
				if !matched {
					return nil, errors.New("redis: native hit changed Core membership")
				}
			}
			distance, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				return nil, err
			}
			score, err := s.schema.score(distance)
			if err != nil {
				return nil, err
			}
			match, err := vectorstore.NewSearchResult(doc, score)
			if err != nil {
				return nil, err
			}
			ranked = append(ranked, rankedHit{result: match, distance: distance})
		}
	}
	slices.SortFunc(ranked, func(left, right rankedHit) int {
		if order := cmp.Compare(left.distance, right.distance); order != 0 {
			return order
		}
		return strings.Compare(left.result.Document.ID, right.result.Document.ID)
	})
	ranked = ranked[:min(len(ranked), request.Options.ResultLimit())]
	var results []*vectorstore.SearchResult
	for _, hit := range ranked {
		if hit.result.Score >= request.Options.MinScore {
			results = append(results, hit.result)
		}
	}
	return &vectorstore.SearchResponse{Results: results}, nil
}

func (s *Store) DeleteIDs(ctx context.Context, ids []string) error {
	var keys []string
	seen := make(map[string]struct{})
	for _, id := range ids {
		if id == "" {
			return vectorstore.ErrMissingDocumentID
		}
		key := s.schema.prefix + id
		if _, duplicate := seen[key]; !duplicate {
			seen[key] = struct{}{}
			keys = append(keys, key)
		}
	}
	for group := range slices.Chunk(keys, filterPageSize) {
		pipe := s.client.Pipeline()
		for _, key := range group {
			pipe.Del(ctx, key)
		}
		commands, err := pipe.Exec(ctx)
		if err != nil {
			return err
		}
		if len(commands) != len(group) {
			return errors.New("redis: explicit deletion omitted acknowledgments")
		}
		for _, command := range commands {
			ack, ok := command.(*goredis.IntCmd)
			if !ok || ack.Val() < 0 || ack.Val() > 1 {
				return errors.New("redis: explicit deletion returned an invalid acknowledgment")
			}
			if err := ack.Err(); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) DeleteWhere(ctx context.Context, predicate filter.Predicate) error {
	if predicate == nil {
		return vectorstore.ErrMissingFilter
	}
	if err := predicate.Validate(); err != nil {
		return err
	}
	records, err := s.matchingRecords(ctx, predicate)
	if err != nil {
		return err
	}
	for _, key := range slices.Sorted(maps.Keys(records)) {
		ack, err := s.client.Eval(ctx, deleteObservedMetadata, []string{key}, metadataField, records[key]).Int64()
		if err != nil {
			return err
		}
		if ack != 1 {
			return errors.New("redis: observed metadata changed before conditional deletion")
		}
	}
	return nil
}

type rankedHit struct {
	result   *vectorstore.SearchResult
	distance float64
}

const deleteObservedMetadata = `if redis.call("HGET", KEYS[1], ARGV[1]) == ARGV[2] then return redis.call("DEL", KEYS[1]) end return 0`

// HSET must succeed before obsolete fields are pruned, preserving the previous
// record on a native wrong-type or allocation failure.
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
