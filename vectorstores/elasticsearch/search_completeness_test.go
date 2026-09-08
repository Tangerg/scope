package elasticsearch

import (
	"strings"
	"testing"
)

// Elasticsearch answers a search that lost shards or ran out of time with 200
// and the surviving hits, so only the response body distinguishes a partial
// index from a small result.
func TestSearchRequiresEveryShard(t *testing.T) {
	for _, sample := range []struct {
		name     string
		response searchResponse
		want     string
	}{
		{
			name:     "all shards answered",
			response: searchResponse{Shards: searchShards{Total: 3}},
		},
		{
			name: "shard failure with a reason",
			response: searchResponse{Shards: searchShards{
				Total: 3, Failed: 1,
				Failures: []searchFailure{{Index: "documents", Shard: 2, Reason: &bulkFailure{Reason: "shard is not available"}}},
			}},
			want: "failed on 1 of 3 shard(s): shard is not available",
		},
		{
			name: "shard failure without a reason",
			response: searchResponse{Shards: searchShards{
				Total: 5, Failed: 2, Failures: []searchFailure{{Index: "documents", Shard: 1}},
			}},
			want: "failed on 2 of 5 shard(s): provider returned no reason",
		},
		{
			name:     "shard failure without a failure list",
			response: searchResponse{Shards: searchShards{Total: 2, Failed: 1}},
			want:     "failed on 1 of 2 shard(s): provider returned no reason",
		},
		{
			name:     "timed out",
			response: searchResponse{TimedOut: true, Shards: searchShards{Total: 3}},
			want:     "timed out and returned partial hits",
		},
	} {
		t.Run(sample.name, func(t *testing.T) {
			store := &Store{indexName: "documents"}
			err := store.checkSearchCompleteness(sample.response)
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
