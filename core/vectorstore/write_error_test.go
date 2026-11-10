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
	err := vectorstore.NewWriteError(cause, vectorstore.WriteOutcome{
		Acknowledged: ids[:1], Rejected: ids[1:2], Unconfirmed: ids[2:3], Unattempted: ids[3:],
		AcknowledgedCount: &count,
	})
	failure, ok := errors.AsType[*vectorstore.WriteError](err)
	if !ok {
		t.Fatal(err)
	}
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

func TestWriteErrorRejectsContradictoryEvidence(t *testing.T) {
	for left := range 4 {
		for right := left; right < 4; right++ {
			outcome := vectorstore.WriteOutcome{}
			partitions := []*[]string{&outcome.Acknowledged, &outcome.Rejected, &outcome.Unconfirmed, &outcome.Unattempted}
			*partitions[left] = append(*partitions[left], "duplicate")
			*partitions[right] = append(*partitions[right], "duplicate")
			cause := errors.New("backend failed")
			err := vectorstore.NewWriteError(cause, outcome)
			if !errors.Is(err, cause) || !errors.Is(err, vectorstore.ErrInvalidWriteOutcome) {
				t.Fatalf("partitions %d/%d: %v", left, right, err)
			}
			if _, ok := errors.AsType[*vectorstore.WriteError](err); ok {
				t.Fatal("invalid evidence became a WriteError")
			}
		}
	}
	for _, count := range []int{-1, 0, 1, 2} {
		err := vectorstore.NewWriteError(nil, vectorstore.WriteOutcome{Unconfirmed: []string{"unknown"}, AcknowledgedCount: &count})
		if invalid := errors.Is(err, vectorstore.ErrInvalidWriteOutcome); invalid != (count < 0 || count > 1) {
			t.Fatalf("count=%d error=%v", count, err)
		}
	}
	if err := vectorstore.NewWriteError(nil, vectorstore.WriteOutcome{Acknowledged: []string{"known"}, AcknowledgedCount: new(1)}); !errors.Is(err, vectorstore.ErrInvalidWriteOutcome) {
		t.Fatalf("aggregate assigned to acknowledged partition: %v", err)
	}
}

func TestInvalidWriteOutcomeRetainsEarlierValidEvidence(t *testing.T) {
	cause := errors.New("backend failed")
	earlier := vectorstore.NewWriteError(cause, vectorstore.WriteOutcome{Acknowledged: []string{"known"}})
	err := vectorstore.NewWriteError(earlier, vectorstore.WriteOutcome{AcknowledgedCount: new(-1)})
	failure, ok := errors.AsType[*vectorstore.WriteError](err)
	if !ok || !errors.Is(err, cause) || !errors.Is(err, vectorstore.ErrInvalidWriteOutcome) || !slices.Equal(failure.Outcome().Acknowledged, []string{"known"}) {
		t.Fatalf("valid evidence or cause lost: %v", err)
	}
}
