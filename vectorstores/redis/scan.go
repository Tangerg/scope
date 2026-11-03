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
if ARGV[1]=='measure' then return bytes end
if bytes~=tonumber(ARGV[1]) then return -1 end
return redis.call('HMGET',KEYS[1],unpack(ARGV,2,#ARGV))
`

func (s *scanGrant) selectRecords(ctx context.Context, store *Store, p filter.Predicate) (map[string]string, error) {
	selected := make(map[string]string)
	seen := make(map[string]bool)
	var mu sync.Mutex
	pattern := strings.NewReplacer("\\", "\\\\", "*", "\\*", "?", "\\?", "[", "\\[", "]", "\\]").Replace(store.schema.prefix) + "*"
	scan := func(ctx context.Context, c hashReader) error {
		cursor := "0"
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			page, err := c.Eval(ctx, scanKeyPage, nil, cursor, pattern, min(filterPageSize, s.grant.MaxRecords), s.grant.MaxRecords, s.grant.MaxBytes).Slice()
			if err != nil {
				return err
			}
			if len(page) == 1 && page[0] == int64(-1) {
				return vectorstore.ErrScanBudget
			}
			if len(page) != 2 {
				return errors.New("redis: malformed SCAN page")
			}
			next, ok := page[0].(string)
			if !ok {
				return errors.New("redis: malformed SCAN cursor")
			}
			if _, err := strconv.ParseUint(next, 10, 64); err != nil {
				return err
			}
			keys, ok := page[1].([]any)
			if !ok {
				return fmt.Errorf("redis: malformed SCAN keys %T", page[1])
			}
			for _, v := range keys {
				key, ok := v.(string)
				if !ok || !strings.HasPrefix(key, store.schema.prefix) || len(key) == len(store.schema.prefix) {
					return errors.New("redis: invalid key in native namespace")
				}
				mu.Lock()
				duplicate := seen[key]
				if !duplicate {
					seen[key] = true
				}
				mu.Unlock()
				if duplicate {
					continue
				}
				size, err := c.Eval(ctx, readHash, []string{key}, "measure", contentField, embeddingField, metadataField, termsField).Int64()
				if err != nil {
					return err
				}
				if size < 0 {
					return errors.New("redis: HASH has unexpected fields")
				}
				if size == 0 {
					if err = s.meter.Charge(int64(len(key))); err != nil {
						return err
					}
					continue
				}
				if err = s.meter.Charge(size); err != nil {
					return err
				}
				raw, err := c.Eval(ctx, readHash, []string{key}, size, contentField, embeddingField, metadataField, termsField).Result()
				if err != nil {
					return err
				}
				values, ok := raw.([]any)
				if !ok || len(values) != 4 {
					return errors.New("redis: HASH changed or has an invalid shape")
				}
				fields := make(map[string]string, 4)
				for i, k := range []string{contentField, embeddingField, metadataField, termsField} {
					str, ok := values[i].(string)
					if !ok {
						return errors.New("redis: mandatory HASH field missing")
					}
					fields[k] = str
				}
				doc, err := store.schema.decode(key, fields)
				if err != nil {
					return err
				}
				facts, err := doc.Metadata.Values()
				if err != nil {
					return err
				}
				accept, err := filter.Match(p, facts)
				if err != nil {
					return err
				}
				if accept {
					mu.Lock()
					selected[key] = fields[metadataField]
					mu.Unlock()
				}
			}
			if next == "0" {
				return nil
			}
			cursor = next
		}
	}
	if err := store.scanNodes(ctx, func(ctx context.Context, client *goredis.Client) error { return scan(ctx, client) }); err != nil {
		return nil, err
	}
	return selected, nil
}
