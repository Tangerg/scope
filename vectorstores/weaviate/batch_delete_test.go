package weaviate

import (
	"strings"
	"testing"

	"github.com/weaviate/weaviate/entities/models"
)

// One batch delete removes at most QUERY_MAXIMUM_RESULTS objects, and the
// response calls Successful the count "in this round". A filter matching more
// than that leaves objects behind, which Weaviate's guidance answers by
// re-running the query — so a round that matched more than it deleted is not
// the end of the work.
func TestBatchDeleteRoundReportsRemainingWork(t *testing.T) {
	t.Parallel()

	for _, sample := range []struct {
		name      string
		results   *models.BatchDeleteResponseResults
		remaining bool
		wantErr   string
	}{
		{
			name:    "everything matched was deleted",
			results: &models.BatchDeleteResponseResults{Matches: 42, Successful: 42, Limit: 10000},
		},
		{
			name:      "matched the per-query maximum",
			results:   &models.BatchDeleteResponseResults{Matches: 25000, Successful: 10000, Limit: 10000},
			remaining: true,
		},
		{
			name:    "some objects could not be deleted",
			results: &models.BatchDeleteResponseResults{Matches: 10, Successful: 7, Failed: 3, Limit: 10000},
			wantErr: "failed on 3 of 10 matched objects",
		},
		{
			name:    "matched but deleted nothing",
			results: &models.BatchDeleteResponseResults{Matches: 5, Successful: 0, Limit: 10000},
			wantErr: "deleted none, so repeating cannot progress",
		},
	} {
		t.Run(sample.name, func(t *testing.T) {
			round, err := batchDeleteRound("Documents", &models.BatchDeleteResponse{Results: sample.results})
			if sample.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), sample.wantErr) {
					t.Fatalf("batchDeleteRound() = %v, want an error containing %q", err, sample.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("batchDeleteRound() = %v, want nil", err)
			}
			if round.remaining() != sample.remaining {
				t.Fatalf("remaining() = %t, want %t", round.remaining(), sample.remaining)
			}
		})
	}
}

// A reply carrying no results says nothing about what was deleted.
func TestBatchDeleteRoundRejectsAnEmptyReply(t *testing.T) {
	t.Parallel()

	for _, response := range []*models.BatchDeleteResponse{nil, {}} {
		if _, err := batchDeleteRound("Documents", response); err == nil {
			t.Fatalf("batchDeleteRound(%#v) = nil, want an error", response)
		}
	}
}
