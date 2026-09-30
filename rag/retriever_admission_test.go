package rag

import (
	"context"
	"errors"
	"testing"
)

func TestParallelResultsStopsAdmissionAndReportsCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	consequence := errors.New("failed after caller cancellation")
	calls := 0
	results, err := parallelResults(ctx, "retrieve", make([]int, 10000), "query", 1,
		func(context.Context, int, int) (int, error) {
			calls++
			cancel()
			return 0, consequence
		})
	if calls != 1 || results != nil || !errors.Is(err, context.Canceled) || errors.Is(err, consequence) {
		t.Fatalf("calls=%d results=%v error=%v", calls, results, err)
	}
}
