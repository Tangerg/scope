package redis

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	goredis "github.com/redis/go-redis/v9"

	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

const (
	measureHashBytes = "measure"
	scanCursorStart  = "0"
)

type scanGrant struct {
	meter *vectorstore.ScanMeter
	grant vectorstore.ScanBudget
}

// SCAN COUNT is only a hint. Reject an oversized page on the master before
// returning key names to the caller. Cursor/control bytes are protocol overhead.
const scanKeyPage = `
local r = redis.call('SCAN',ARGV[1],'MATCH',ARGV[2],'COUNT',ARGV[3])
if #r[2] > tonumber(ARGV[4]) then return {-1} end
local n=0
for _,key in ipairs(r[2]) do n=n+#key end
if n > tonumber(ARGV[5]) then return {-1} end
return {r[1],r[2]}
`

// Both the size probe and the subsequent read use the same atomic size check.
// Only the fixed current fields can cross the wire. A concurrent size change
// invalidates admission; equal-size replacements are legal (this is not a snapshot).
const readHash = `
local count = redis.call('HLEN',KEYS[1])
if count==0 then return 0 end
if count~=#ARGV-1 then return -1 end
local bytes=#KEYS[1]
for i=2,#ARGV do bytes=bytes+#ARGV[i]+redis.call('HSTRLEN',KEYS[1],ARGV[i]) end
if ARGV[1]=='` + measureHashBytes + `' then return bytes end
if bytes~=tonumber(ARGV[1]) then return -1 end
return redis.call('HMGET',KEYS[1],unpack(ARGV,2,#ARGV))
`

func (s *scanGrant) selectRecords(ctx context.Context, store *Store, p filter.Predicate) (map[string]string, error) {
	scan := &keyScan{grant: s, store: store, predicate: p, seen: make(map[string]bool), selected: make(map[string]string)}
	if err := store.scanNodes(ctx, func(ctx context.Context, client *goredis.Client) error { return scan.node(ctx, client) }); err != nil {
		return nil, err
	}
	return scan.selected, nil
}

// keyScan selects the namespace's documents that satisfy a predicate across
// every node. Nodes run concurrently, so seen and selected are guarded by mu.
type keyScan struct {
	grant     *scanGrant
	store     *Store
	predicate filter.Predicate
	mu        sync.Mutex
	seen      map[string]bool
	selected  map[string]string
}

// node walks one node's keyspace; SCAN starts and ends at cursor zero.
func (k *keyScan) node(ctx context.Context, c hashReader) error {
	pattern := strings.NewReplacer("\\", "\\\\", "*", "\\*", "?", "\\?", "[", "\\[", "]", "\\]").Replace(k.store.schema.prefix) + "*"
	cursor := scanCursorStart
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		next, keys, err := k.page(ctx, c, cursor, pattern)
		if err != nil {
			return err
		}
		for _, key := range keys {
			if !k.claim(key) {
				continue
			}
			if err := k.consider(ctx, c, key); err != nil {
				return err
			}
		}
		if next == scanCursorStart {
			return nil
		}
		cursor = next
	}
}

func (k *keyScan) page(ctx context.Context, c hashReader, cursor, pattern string) (string, []string, error) {
	budget := k.grant.grant
	page, err := c.Eval(ctx, scanKeyPage, nil, cursor, pattern, min(filterPageSize, budget.MaxRecords), budget.MaxRecords, budget.MaxBytes).Slice()
	if err != nil {
		return "", nil, err
	}
	if len(page) == 1 && page[0] == int64(-1) {
		return "", nil, vectorstore.ErrScanBudget
	}
	if len(page) != 2 {
		return "", nil, errors.New("redis: malformed SCAN page")
	}
	next, ok := page[0].(string)
	if !ok {
		return "", nil, errors.New("redis: malformed SCAN cursor")
	}
	if _, err := strconv.ParseUint(next, 10, 64); err != nil {
		return "", nil, err
	}
	values, ok := page[1].([]any)
	if !ok {
		return "", nil, fmt.Errorf("redis: malformed SCAN keys %T", page[1])
	}
	keys := make([]string, len(values))
	for index, value := range values {
		key, ok := value.(string)
		if !ok || !strings.HasPrefix(key, k.store.schema.prefix) || len(key) == len(k.store.schema.prefix) {
			return "", nil, errors.New("redis: invalid key in native namespace")
		}
		keys[index] = key
	}
	return next, keys, nil
}

// claim reports whether key is new; SCAN may return a key more than once.
func (k *keyScan) claim(key string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.seen[key] {
		return false
	}
	k.seen[key] = true
	return true
}

// consider charges the grant for one HASH before reading it, then selects the
// document when it satisfies the predicate. An empty HASH costs its key.
func (k *keyScan) consider(ctx context.Context, c hashReader, key string) error {
	size, err := c.Eval(ctx, readHash, []string{key}, measureHashBytes, contentField, embeddingField, metadataField, termsField).Int64()
	if err != nil {
		return err
	}
	if size < 0 {
		return errors.New("redis: HASH has unexpected fields")
	}
	if size == 0 {
		return k.grant.meter.Charge(int64(len(key)))
	}
	if err = k.grant.meter.Charge(size); err != nil {
		return err
	}
	fields, err := k.read(ctx, c, key, size)
	if err != nil {
		return err
	}
	doc, err := k.store.schema.decode(key, fields)
	if err != nil {
		return err
	}
	accept, err := filter.Match(k.predicate, doc.Metadata)
	if err != nil || !accept {
		return err
	}
	k.mu.Lock()
	k.selected[key] = fields[metadataField]
	k.mu.Unlock()
	return nil
}

func (k *keyScan) read(ctx context.Context, c hashReader, key string, size int64) (map[string]string, error) {
	raw, err := c.Eval(ctx, readHash, []string{key}, size, contentField, embeddingField, metadataField, termsField).Result()
	if err != nil {
		return nil, err
	}
	values, ok := raw.([]any)
	if !ok || len(values) != 4 {
		return nil, errors.New("redis: HASH changed or has an invalid shape")
	}
	fields := make(map[string]string, 4)
	for index, name := range []string{contentField, embeddingField, metadataField, termsField} {
		value, ok := values[index].(string)
		if !ok {
			return nil, errors.New("redis: mandatory HASH field missing")
		}
		fields[name] = value
	}
	return fields, nil
}
