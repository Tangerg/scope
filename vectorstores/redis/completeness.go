package redis

import (
	"fmt"
	"strings"

	goredis "github.com/redis/go-redis/v9"
)

// checkSearchCompleteness rejects a truncated FT.SEARCH answer. RediSearch runs
// every query under a timeout whose default ON_TIMEOUT policy returns the hits
// gathered so far and reports the truncation as a warning instead of an error,
// so a short hit list is otherwise indistinguishable from a small match set. A
// per-document error means that hit could not be materialized, which would
// otherwise disappear from a search result or escape a filtered deletion.
func checkSearchCompleteness(indexName string, result goredis.FTSearchResult) error {
	if len(result.Warnings) > 0 {
		return fmt.Errorf("redis: FT.SEARCH %s returned a partial result: %s",
			indexName, strings.Join(result.Warnings, "; "))
	}
	for _, hit := range result.Docs {
		if hit.Error != nil {
			return fmt.Errorf("redis: FT.SEARCH %s hit %q: %w", indexName, hit.ID, hit.Error)
		}
	}
	return nil
}
