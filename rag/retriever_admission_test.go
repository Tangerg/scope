package rag

import (
	"context"
	"errors"
	"testing"
)

func TestParallelResultsStopsAdmissionAndCollectsActiveFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cause := errors.New("active retrieval failed")
	calls := 0
	results, err := parallelResults(ctx, "retrieve", make([]int, 10000), "query", 1,
		func(context.Context, int, int) (int, error) {
			calls++
			cancel()
			return 0, cause
		})
	if calls != 1 || results != nil || !errors.Is(err, cause) || !errors.Is(err, context.Canceled) {
		t.Fatalf("calls=%d results=%v error=%v", calls, results, err)
	}
}
