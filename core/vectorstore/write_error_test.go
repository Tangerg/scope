package vectorstore_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/Tangerg/scope/core/vectorstore"
)

func TestWriteErrorCloneOwnsEveryPartition(t *testing.T) {
	ids := []string{"accepted", "rejected", "unknown", "untouched"}
	count := 1
	cause := errors.New("unconfirmed write")
	original := vectorstore.WriteError{
		Acknowledged: ids[:1], Rejected: ids[1:2], Unconfirmed: ids[2:3], Unattempted: ids[3:],
		AcknowledgedCount: &count, Err: cause,
	}
	clone := original.Clone()
	ids[0], count = "changed", 0
	merged := append(clone.Acknowledged, "later operation")
	if !slices.Equal(merged, []string{"accepted", "later operation"}) ||
		!slices.Equal(clone.Acknowledged, []string{"accepted"}) ||
		!slices.Equal(clone.Rejected, []string{"rejected"}) ||
		!slices.Equal(clone.Unconfirmed, []string{"unknown"}) ||
		!slices.Equal(clone.Unattempted, []string{"untouched"}) || *clone.AcknowledgedCount != 1 || !errors.Is(clone, cause) {
		t.Fatalf("cloned evidence changed with another owner: %+v", clone)
	}
	clone.Unconfirmed[0] = "edited copy"
	if ids[2] != "unknown" {
		t.Fatal("editing the clone changed the original evidence")
	}
}
