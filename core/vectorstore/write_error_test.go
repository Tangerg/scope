package vectorstore_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/Tangerg/scope/core/vectorstore"
)

func TestWriteErrorOwnsEveryPartition(t *testing.T) {
	ids := []string{"accepted", "rejected", "unknown", "untouched"}
	count := 1
	cause := errors.New("unconfirmed write")
	failure := vectorstore.NewWriteError(cause, vectorstore.WriteOutcome{
		Acknowledged: ids[:1], Rejected: ids[1:2], Unconfirmed: ids[2:3], Unattempted: ids[3:],
		AcknowledgedCount: &count,
	})
	ids[0], count = "changed", 0
	outcome := failure.Outcome()
	merged := append(outcome.Acknowledged, "later operation")
	if !slices.Equal(merged, []string{"accepted", "later operation"}) ||
		!slices.Equal(outcome.Acknowledged, []string{"accepted"}) ||
		!slices.Equal(outcome.Rejected, []string{"rejected"}) ||
		!slices.Equal(outcome.Unconfirmed, []string{"unknown"}) ||
		!slices.Equal(outcome.Unattempted, []string{"untouched"}) || *outcome.AcknowledgedCount != 1 || !errors.Is(failure, cause) {
		t.Fatalf("evidence changed with another owner: %+v", outcome)
	}
	outcome.Unconfirmed[0] = "edited copy"
	if again := failure.Outcome(); again.Unconfirmed[0] != "unknown" || ids[2] != "unknown" {
		t.Fatal("editing a reader's copy changed the error's evidence")
	}
}
