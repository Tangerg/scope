package redis

import (
	"context"
	"errors"
	"fmt"

	goredis "github.com/redis/go-redis/v9"

	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

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
		if err := checkSearchCompleteness(s.indexName, result); err != nil {
			return err
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
