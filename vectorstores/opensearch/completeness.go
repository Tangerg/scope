package opensearch

import (
	"fmt"

	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
)

// checkSearchCompleteness rejects a result assembled from fewer shards than the
// query targeted. OpenSearch answers a search that lost shards or ran out of
// time with 200 and the surviving hits, so a caller reading only the status
// cannot tell a partial index from a small result. Skipped shards are a normal
// pre-filtering outcome and stay acceptable.
func (s *Store) checkSearchCompleteness(response *opensearchapi.SearchResp) error {
	if response.Shards.Failed > 0 {
		reason := "provider returned no reason"
		if len(response.Shards.Failures) > 0 {
			if stated := response.Shards.Failures[0].Reason.Reason; stated != "" {
				reason = stated
			}
		}
		return fmt.Errorf("opensearch: search %s failed on %d of %d shard(s): %s",
			s.indexName, response.Shards.Failed, response.Shards.Total, reason)
	}
	if response.Timeout {
		return fmt.Errorf("opensearch: search %s timed out and returned partial hits", s.indexName)
	}
	return nil
}

// checkDeleteByQueryCompleteness rejects a deletion that left matching
// documents behind. Version conflicts, query timeouts, and per-document
// failures are all reported inside a successful delete_by_query response, and
// Total counts matched candidates while Deleted counts applied deletions.
func (s *Store) checkDeleteByQueryCompleteness(response *opensearchapi.DocumentDeleteByQueryResp) error {
	if len(response.Failures) > 0 {
		failure := response.Failures[0]
		reason := "provider returned no reason"
		if failure.Cause != nil && failure.Cause.Reason != "" {
			reason = failure.Cause.Reason
		} else if failure.Reason != nil && failure.Reason.Reason != "" {
			reason = failure.Reason.Reason
		}
		return fmt.Errorf("opensearch: delete_by_query %s failed for document %q with status %d: %s",
			s.indexName, failure.ID, failure.Status, reason)
	}
	if response.VersionConflicts != 0 {
		return fmt.Errorf("opensearch: delete_by_query %s left %d document(s) on version conflict",
			s.indexName, response.VersionConflicts)
	}
	if response.TimedOut {
		return fmt.Errorf("opensearch: delete_by_query %s timed out after deleting %d of %d document(s)",
			s.indexName, response.Deleted, response.Total)
	}
	if response.Deleted != response.Total {
		return fmt.Errorf("opensearch: delete_by_query %s deleted %d of %d matched document(s)",
			s.indexName, response.Deleted, response.Total)
	}
	return nil
}
