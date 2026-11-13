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

	goredis "github.com/redis/go-redis/v9"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/filter/keyword"
)

const (
	redisSearchDialectVersion = 2
	filterPageSize            = 256
	// allDocumentsPrefix is the KNN pre-filter that admits every document.
	allDocumentsPrefix = "*"
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

func (s *Store) readSchema(ctx context.Context) (nativeSchema, error) {
	raw, err := s.client.Do(ctx, "FT.INFO", s.indexName).Result()
	if err != nil {
		return nativeSchema{}, err
	}
	info, err := parseIndexInfo(raw)
	if err != nil {
		return nativeSchema{}, fmt.Errorf("%w: %w", ErrIncompatibleIndex, err)
	}
	if info.name != s.indexName || info.keyType != nativeHashKeyType || len(info.prefixes) != 1 || info.prefixes[0] == "" || info.filtered {
		return nativeSchema{}, fmt.Errorf("%w: requires one concrete HASH namespace without FILTER", ErrIncompatibleIndex)
	}
	if !strings.HasSuffix(info.prefixes[0], s.space.StorageKey()+":") {
		return nativeSchema{}, vectorstore.ErrIncompatibleSpace
	}
	schema := nativeSchema{prefix: info.prefixes[0]}
	if err := schema.readAttributes(info.attributes); err != nil {
		return nativeSchema{}, err
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
	ids, rows, err := s.hashRecords(b.Records)
	if err != nil {
		return err
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
		commands, execErr := pipe.Exec(ctx)
		outcome, writeErr := acknowledgeReplacements(commands, ids[offset:offset+len(group)], execErr)
		if writeErr != nil {
			outcome.Acknowledged = append(slices.Clone(ids[:offset]), outcome.Acknowledged...)
			outcome.Unattempted = ids[offset+len(group):]
			return vectorstore.NewWriteError(writeErr, outcome)
		}
		offset += len(group)
	}
	return nil
}

func (s *Store) hashRecords(records []vectorstore.Record) ([]string, []hashRecord, error) {
	ids := make([]string, len(records))
	rows := make([]hashRecord, len(records))
	for i, r := range records {
		doc := r.Document
		if doc.Media != nil {
			return nil, nil, vectorstore.ErrInvalidDocument
		}
		facts, err := doc.MarshalStoredMetadata()
		if err != nil {
			return nil, nil, err
		}
		terms, err := tagValues(doc.Metadata)
		if err != nil {
			return nil, nil, err
		}
		vector, err := s.schema.vector(r.Values)
		if err != nil {
			return nil, nil, err
		}
		ids[i] = doc.ID
		rows[i] = hashRecord{key: s.schema.prefix + doc.ID, content: doc.Text, metadata: string(facts), terms: terms, vector: vector}
	}
	return ids, rows, nil
}

// acknowledgeReplacements splits one pipelined group by its replies: the
// replacement script answers 1 for each HASH it wrote, and any other or
// missing reply leaves that document unconfirmed.
func acknowledgeReplacements(commands []goredis.Cmder, ids []string, err error) (vectorstore.WriteOutcome, error) {
	var outcome vectorstore.WriteOutcome
	if len(commands) != len(ids) {
		err = errors.Join(err, errors.New("redis: incomplete pipeline reply"))
	}
	for i, id := range ids {
		confirmed := false
		if i < len(commands) {
			if c, ok := commands[i].(*goredis.Cmd); ok && c != nil {
				n, replyErr := c.Int64()
				confirmed = replyErr == nil && n == 1
				err = errors.Join(err, replyErr)
			}
		}
		if confirmed {
			outcome.Acknowledged = append(outcome.Acknowledged, id)
		} else {
			outcome.Unconfirmed = append(outcome.Unconfirmed, id)
		}
	}
	if err == nil && len(outcome.Unconfirmed) != 0 {
		err = errors.New("redis: HASH replacement not acknowledged")
	}
	return outcome, err
}

func (s *Store) queryPrefix(p filter.Predicate) (string, error) {
	if p == nil {
		return allDocumentsPrefix, nil
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
	if err := opts.Validate(); err != nil {
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
	if err := q.ValidateFor(s.space); err != nil {
		return nil, err
	}
	return s.rank(ctx, q, nil)
}

func (s *Store) QueryByScan(ctx context.Context, q *vectorstore.Query, b vectorstore.ScanBudget) (*vectorstore.SearchResponse, error) {
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

// rank expects a request its entry point already validated for this store.
func (s *Store) rank(ctx context.Context, request *vectorstore.Query, scan *scanGrant) (response *vectorstore.SearchResponse, err error) {
	if err = ctx.Err(); err != nil {
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
	prefix := allDocumentsPrefix
	if scan == nil {
		if prefix, err = s.queryPrefix(request.Options.Filter); err != nil {
			return nil, err
		}
	}
	if err = s.verify(ctx); err != nil {
		return nil, err
	}
	groups, err := s.keyGroups(ctx, request.Options.Filter, scan)
	if err != nil {
		return nil, err
	}
	search := groupSearch{store: s, options: request.Options, prefix: prefix, query: query, scanned: scan != nil, seen: make(map[string]struct{})}
	var ranked []rankedHit
	for _, group := range groups {
		hits, err := search.rank(ctx, group)
		if err != nil {
			return nil, err
		}
		ranked = append(ranked, hits...)
	}
	slices.SortFunc(ranked, func(left, right rankedHit) int {
		return cmp.Or(cmp.Compare(left.distance, right.distance), strings.Compare(left.result.Document.ID, right.result.Document.ID))
	})
	var results []*vectorstore.SearchResult
	for _, hit := range ranked[:min(len(ranked), request.Options.ResultLimit())] {
		if hit.result.Score >= request.Options.MinScore {
			results = append(results, hit.result)
		}
	}
	return &vectorstore.SearchResponse{Results: results}, nil
}

// keyGroups bounds each native search: one unrestricted group for a native
// query, otherwise the scanned members in groups INKEYS can carry.
func (s *Store) keyGroups(ctx context.Context, predicate filter.Predicate, scan *scanGrant) ([][]string, error) {
	if scan == nil {
		return [][]string{nil}, nil
	}
	records, err := scan.selectRecords(ctx, s, predicate)
	if err != nil {
		return nil, err
	}
	return slices.Collect(slices.Chunk(slices.Sorted(maps.Keys(records)), filterPageSize)), nil
}

// groupSearch runs one query's KNN searches. seen spans every group, so a key
// returned twice is rejected even across groups.
type groupSearch struct {
	store   *Store
	options vectorstore.SearchOptions
	prefix  string
	query   []byte
	scanned bool
	seen    map[string]struct{}
}

func (g groupSearch) rank(ctx context.Context, keys []string) ([]rankedHit, error) {
	raw, err := g.store.client.Do(ctx, g.command(keys)...).Result()
	if err != nil {
		return nil, err
	}
	result, err := parseSearchResult(raw)
	if err != nil {
		return nil, err
	}
	if len(result.Docs) != min(result.Total, g.options.ResultLimit()) {
		return nil, errors.New("redis: native result count does not acknowledge complete hits")
	}
	ranked := make([]rankedHit, 0, len(result.Docs))
	for _, hit := range result.Docs {
		if _, duplicate := g.seen[hit.ID]; duplicate {
			return nil, errors.New("redis: native search repeated a document key")
		}
		g.seen[hit.ID] = struct{}{}
		admitted, err := g.admit(hit, keys)
		if err != nil {
			return nil, err
		}
		ranked = append(ranked, admitted)
	}
	return ranked, nil
}

// command is one FT.SEARCH KNN query, restricted to keys when it has any.
func (g groupSearch) command(keys []string) []any {
	limit := g.options.ResultLimit()
	args := []any{"FT.SEARCH", g.store.indexName, fmt.Sprintf("%s=>[KNN %d @%s $%s AS %s]", g.prefix, limit, embeddingField, vectorParamName, distanceFieldName)}
	if len(keys) > 0 {
		args = append(args, "INKEYS", len(keys))
		for _, key := range keys {
			args = append(args, key)
		}
	}
	return append(args, "RETURN", 5, contentField, embeddingField, metadataField, termsField, distanceFieldName, "SORTBY", distanceFieldName, "ASC", "LIMIT", 0, limit, "PARAMS", 2, vectorParamName, g.query, "DIALECT", redisSearchDialectVersion)
}

// admit decodes one hit and rechecks a filtered hit against Core membership,
// because the native index may have changed since the scan selected it.
func (g groupSearch) admit(hit goredis.Document, keys []string) (rankedHit, error) {
	fields := maps.Clone(hit.Fields)
	raw, ok := fields[distanceFieldName]
	if !ok {
		return rankedHit{}, errors.New("redis: native hit is missing its distance")
	}
	delete(fields, distanceFieldName)
	doc, err := g.store.schema.decode(hit.ID, fields)
	if err != nil {
		return rankedHit{}, err
	}
	if g.options.Filter != nil {
		if g.scanned && !slices.Contains(keys, hit.ID) {
			return rankedHit{}, errors.New("redis: native hit is outside Core membership")
		}
		matched, matchErr := filter.Match(g.options.Filter, doc.Metadata)
		if matchErr != nil {
			return rankedHit{}, matchErr
		}
		if !matched {
			return rankedHit{}, errors.New("redis: native hit changed Core membership")
		}
	}
	distance, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return rankedHit{}, err
	}
	score, err := g.store.schema.score(distance)
	if err != nil {
		return rankedHit{}, err
	}
	match, err := vectorstore.NewSearchResult(doc, score)
	if err != nil {
		return rankedHit{}, err
	}
	return rankedHit{result: match, distance: distance}, nil
}

func (s *Store) DeleteIDs(ctx context.Context, ids []string) error {
	ids, err := vectorstore.DistinctIDs(ids)
	if err != nil {
		return err
	}
	var keys []string
	for _, id := range ids {
		keys = append(keys, s.schema.prefix+id)
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
