package opensearch

import (
	"strings"
	"testing"

	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
)

func shardFailure(reason string) opensearchapi.ResponseShardsFailure {
	failure := opensearchapi.ResponseShardsFailure{Shard: 1, Node: "node-1"}
	failure.Reason.Reason = reason
	return failure
}

// OpenSearch answers a search that lost shards or ran out of time with 200 and
// the surviving hits, so only the response body separates a partial index from
// a small result.
func TestSearchRequiresEveryShard(t *testing.T) {
	for _, sample := range []struct {
		name     string
		response opensearchapi.SearchResp
		want     string
	}{
		{
			name:     "all shards answered",
			response: opensearchapi.SearchResp{Shards: opensearchapi.ResponseShards{Total: 3, Successful: 3}},
		},
		{
			name: "skipped shards stay acceptable",
			response: opensearchapi.SearchResp{
				Shards: opensearchapi.ResponseShards{Total: 3, Successful: 2, Skipped: 1},
			},
		},
		{
			name: "shard failure with a reason",
			response: opensearchapi.SearchResp{Shards: opensearchapi.ResponseShards{
				Total: 3, Failed: 1,
				Failures: []opensearchapi.ResponseShardsFailure{shardFailure("shard is not available")},
			}},
			want: "failed on 1 of 3 shard(s): shard is not available",
		},
		{
			name: "shard failure without a reason",
			response: opensearchapi.SearchResp{Shards: opensearchapi.ResponseShards{
				Total: 2, Failed: 1,
			}},
			want: "failed on 1 of 2 shard(s): provider returned no reason",
		},
		{
			name: "timed out",
			response: opensearchapi.SearchResp{
				Timeout: true, Shards: opensearchapi.ResponseShards{Total: 3, Successful: 3},
			},
			want: "timed out and returned partial hits",
		},
	} {
		t.Run(sample.name, func(t *testing.T) {
			store := &Store{indexName: "documents"}
			err := store.checkSearchCompleteness(&sample.response)
			if sample.want == "" {
				if err != nil {
					t.Fatalf("checkSearchCompleteness() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), sample.want) {
				t.Fatalf("checkSearchCompleteness() = %v, want an error containing %q", err, sample.want)
			}
		})
	}
}

// Version conflicts and query timeouts leave matching documents in place while
// delete_by_query still answers successfully.
func TestDeleteByQueryRequiresCompleteDeletion(t *testing.T) {
	for _, sample := range []struct {
		name     string
		response opensearchapi.DocumentDeleteByQueryResp
		want     string
	}{
		{
			name:     "complete",
			response: opensearchapi.DocumentDeleteByQueryResp{Total: 2, Deleted: 2},
		},
		{
			name:     "nothing matched",
			response: opensearchapi.DocumentDeleteByQueryResp{},
		},
		{
			name: "per document failure",
			response: opensearchapi.DocumentDeleteByQueryResp{
				Total: 2, Deleted: 1,
				Failures: []opensearchapi.BulkByScrollFailure{{
					ID: "two", Status: 409,
					Cause: &opensearchapi.DocumentError{Reason: "version conflict"},
				}},
			},
			want: `failed for document "two" with status 409: version conflict`,
		},
		{
			name:     "version conflicts",
			response: opensearchapi.DocumentDeleteByQueryResp{Total: 8, VersionConflicts: 8},
			want:     "left 8 document(s) on version conflict",
		},
		{
			name:     "timed out",
			response: opensearchapi.DocumentDeleteByQueryResp{TimedOut: true, Total: 9, Deleted: 4},
			want:     "timed out after deleting 4 of 9 document(s)",
		},
		{
			name:     "short deletion",
			response: opensearchapi.DocumentDeleteByQueryResp{Total: 5, Deleted: 3},
			want:     "deleted 3 of 5 matched document(s)",
		},
	} {
		t.Run(sample.name, func(t *testing.T) {
			store := &Store{indexName: "documents"}
			err := store.checkDeleteByQueryCompleteness(&sample.response)
			if sample.want == "" {
				if err != nil {
					t.Fatalf("checkDeleteByQueryCompleteness() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), sample.want) {
				t.Fatalf("checkDeleteByQueryCompleteness() = %v, want an error containing %q", err, sample.want)
			}
		})
	}
}
