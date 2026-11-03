package redis

import (
	"cmp"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	goredis "github.com/redis/go-redis/v9"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/filter/keyword"
)

const (
	redisSearchDialectVersion = 2
	filterPageSize            = 256
)

var ErrIncompatibleIndex = errors.New("redis: native index is incompatible")
var (
	_ vectorstore.Upserter  = (*Store)(nil)
	_ vectorstore.Querier   = (*Store)(nil)
	_ vectorstore.IDDeleter = (*Store)(nil)
)

type Store struct {
	client         RedisClient
	scanNodes      func(context.Context, func(context.Context, *goredis.Client) error) error
	indexName      string
	schema         nativeSchema
	space          vectorstore.SpaceID
	writeBatchSize int
}

func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	s := &Store{client: config.Client, scanNodes: config.ScanNodes, indexName: cmp.Or(config.IndexName, DefaultIndexName), space: config.Space, writeBatchSize: cmp.Or(config.WriteBatchSize, DefaultWriteBatchSize)}
	var err error
	s.schema, err = s.readSchema(ctx)
	if err != nil {
		return nil, err
	}
	return s, nil
}
func (s *Store) readSchema(ctx context.Context) (schema nativeSchema, err error) {
	raw, err := s.client.Do(ctx, "FT.INFO", s.indexName).Result()
	if err != nil {
		return schema, err
	}
	info, err := parseIndexInfo(raw)
	if err != nil {
		return schema, fmt.Errorf("%w: %w", ErrIncompatibleIndex, err)
	}
	if info.name != s.indexName || info.keyType != "HASH" || len(info.prefixes) != 1 || info.prefixes[0] == "" || info.filtered {
		return schema, fmt.Errorf("%w: requires one concrete HASH namespace without FILTER", ErrIncompatibleIndex)
	}
	if !strings.HasSuffix(info.prefixes[0], s.space.StorageKey()+":") {
		return schema, vectorstore.ErrIncompatibleSpace
	}
	var vectorFound bool
	for _, attribute := range info.attributes {
		if attribute.Identifier != attribute.Attribute {
			return schema, fmt.Errorf("%w: native aliases are unsupported", ErrIncompatibleIndex)
		}
		if attribute.Attribute == termsField && attribute.Type == "TAG" {
			schema.indexedTerms = true
			continue
		}
		if attribute.Attribute == contentField && attribute.Type == "TEXT" {
			continue
		}
		if vectorFound || attribute.Attribute != embeddingField || attribute.Type != "VECTOR" || attribute.DataType != "FLOAT32" || attribute.Dim <= 0 {
			return schema, fmt.Errorf("%w: requires one FLOAT32 embedding field", ErrIncompatibleIndex)
		}
		switch nativeMetric(attribute.DistanceMetric) {
		case metricCosine, metricL2, metricIP:
		default:
			return schema, fmt.Errorf("%w: unsupported distance %q", ErrIncompatibleIndex, attribute.DistanceMetric)
		}
		schema.prefix = info.prefixes[0]
		schema.dimensions = attribute.Dim
		schema.metric = nativeMetric(attribute.DistanceMetric)
		vectorFound = true
	}
	if !vectorFound {
		return schema, fmt.Errorf("%w: embedding field is missing", ErrIncompatibleIndex)
	}
	return schema, nil
}

func (s *Store) verify(ctx context.Context) error {
	schema, err := s.readSchema(ctx)
	if err != nil {
		return err
	}
	if schema != s.schema {
		return fmt.Errorf("%w: index shape changed", vectorstore.ErrIncompatibleSpace)
	}
	return nil
}
func (s *Store) CheckDocuments(ctx context.Context, space vectorstore.SpaceID, docs []*document.Document) error {
	if err := space.Require(s.space); err != nil {
		return err
	}
	if err := vectorstore.ValidateDocuments(docs); err != nil {
		return err
	}
	for _, doc := range docs {
		if doc.Media != nil {
			return vectorstore.ErrInvalidDocument
		}
	}
	return s.verify(ctx)
}
func (s *Store) Upsert(ctx context.Context, b *vectorstore.Batch) error {
	if err := b.ValidateFor(s.space); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	rows := make([]hashRecord, len(b.Records))
	ids := make([]string, len(rows))
	for i, r := range b.Records {
		doc := r.Document
		if doc.Media != nil {
			return vectorstore.ErrInvalidDocument
		}
		facts, err := document.MarshalStoredMetadata(doc)
		if err != nil {
			return err
		}
		terms, err := tagValues(doc.Metadata)
		if err != nil {
			return err
		}
		vector, err := s.schema.vector(r.Values)
		if err != nil {
			return err
		}
		ids[i] = doc.ID
		rows[i] = hashRecord{key: s.schema.prefix + doc.ID, content: doc.Text, metadata: string(facts), terms: terms, vector: vector}
	}
	if err := s.verify(ctx); err != nil {
		return err
	}
	offset := 0
	for group := range slices.Chunk(rows, s.writeBatchSize) {
		pipe := s.client.Pipeline()
		for _, r := range group {
			pipe.Eval(ctx, replaceDocumentHash, []string{r.key}, contentField, r.content, embeddingField, r.vector, metadataField, r.metadata, termsField, r.terms)
		}
		commands, err := pipe.Exec(ctx)
		w := &vectorstore.WriteError{Err: err}
		if len(commands) != len(group) {
			w.Err = errors.Join(w.Err, errors.New("redis: incomplete pipeline reply"))
		}
		for i := range group {
			var confirmed bool
			if i < len(commands) {
				if c, ok := commands[i].(*goredis.Cmd); ok && c != nil {
					n, e := c.Int64()
					confirmed = e == nil && n == 1
					if e != nil {
						w.Err = errors.Join(w.Err, e)
					}
				}
			}
			if confirmed {
				w.Acknowledged = append(w.Acknowledged, ids[offset+i])
			} else {
				w.Unconfirmed = append(w.Unconfirmed, ids[offset+i])
			}
		}
		if w.Err != nil || len(w.Unconfirmed) != 0 {
			if w.Err == nil {
				w.Err = errors.New("redis: HASH replacement not acknowledged")
			}
			w.Acknowledged = append(slices.Clone(ids[:offset]), w.Acknowledged...)
			w.Unattempted = slices.Clone(ids[offset+len(group):])
			return w
		}
		offset += len(group)
	}
	return nil
}
func (s *Store) queryPrefix(p filter.Predicate) (string, error) {
	if p == nil {
		return "*", nil
	}
	if !s.schema.indexedTerms {
		return "", vectorstore.ErrUnsupportedFilter
	}
	t, err := keyword.EqualString(p)
	if err != nil {
		return "", err
	}
	return "(@" + termsField + ":{" + hex.EncodeToString([]byte(t)) + "})", nil
}
func (s *Store) CheckQuery(ctx context.Context, space vectorstore.SpaceID, opts vectorstore.SearchOptions) error {
	if err := space.Require(s.space); err != nil {
		return err
	}
	if err := opts.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return err
	}
	if _, err := s.queryPrefix(opts.Filter); err != nil {
		return err
	}
	return s.verify(ctx)
}
func (s *Store) Query(ctx context.Context, q *vectorstore.Query) (*vectorstore.SearchResponse, error) {
	return s.rank(ctx, q, nil)
}
func (s *Store) Scan(ctx context.Context, q *vectorstore.Query, b vectorstore.ScanBudget) (*vectorstore.SearchResponse, error) {
	if err := q.ValidateFor(s.space); err != nil {
		return nil, err
	}
	if q.Options.Filter == nil {
		return nil, vectorstore.ErrMissingFilter
	}
	meter, err := b.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer meter.Close()
	return s.rank(meter.Context(), q, &scanGrant{meter: meter, grant: b})
}
func (s *Store) rank(ctx context.Context, request *vectorstore.Query, scan *scanGrant) (response *vectorstore.SearchResponse, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = request.ValidateFor(s.space); err != nil {
		return nil, err
	}
	if err = request.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, err
	}
	defer func() {
		if err == nil {
			err = request.ValidateResponse(response)
		}
		if err != nil {
			response = nil
		}
	}()
	query, err := s.schema.vector(request.Values)
	if err != nil {
		return nil, err
	}
	prefix := "*"
	if scan == nil {
		prefix, err = s.queryPrefix(request.Options.Filter)
		if err != nil {
			return nil, err
		}
	}
	if err := s.verify(ctx); err != nil {
		return nil, err
	}
	groups := [][]string{nil}
	if scan != nil {
		records, err := scan.selectRecords(ctx, s, request.Options.Filter)
		if err != nil {
			return nil, err
		}
		if len(records) == 0 {
			return &vectorstore.SearchResponse{}, nil
		}
		groups = slices.Collect(slices.Chunk(slices.Sorted(maps.Keys(records)), filterPageSize))
	}
	var ranked []rankedHit
	seen := make(map[string]struct{})
	for _, group := range groups {
		args := []any{"FT.SEARCH", s.indexName, fmt.Sprintf("%s=>[KNN %d @%s $%s AS %s]", prefix, request.Options.ResultLimit(), embeddingField, vectorParamName, distanceFieldName)}
		if len(group) > 0 {
			args = append(args, "INKEYS", len(group))
			for _, key := range group {
				args = append(args, key)
			}
		}
		args = append(args, "RETURN", 5, contentField, embeddingField, metadataField, termsField, distanceFieldName, "SORTBY", distanceFieldName, "ASC", "LIMIT", 0, request.Options.ResultLimit(), "PARAMS", 2, vectorParamName, query, "DIALECT", redisSearchDialectVersion)
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
				if scan != nil && !slices.Contains(group, hit.ID) {
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
		if strings.TrimSpace(id) == "" || !utf8.ValidString(id) {
			return vectorstore.ErrMissingDocumentID
		}
		key := s.schema.prefix + id
		if _, duplicate := seen[key]; !duplicate {
			seen[key] = struct{}{}
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	if err := s.verify(ctx); err != nil {
		return err
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

func (s *Store) DeleteMatching(ctx context.Context, predicate filter.Predicate, b vectorstore.ScanBudget) error {
	if predicate == nil {
		return vectorstore.ErrMissingFilter
	}
	if err := predicate.Validate(); err != nil {
		return err
	}
	meter, err := b.Begin(ctx)
	if err != nil {
		return err
	}
	defer meter.Close()
	ctx = meter.Context()
	if verifyErr := s.verify(ctx); verifyErr != nil {
		return verifyErr
	}
	records, err := (&scanGrant{meter: meter, grant: b}).selectRecords(ctx, s, predicate)
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
