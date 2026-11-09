package managed

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent/run"
)

// A storage outage while confirming an answer must stay retryable; only a
// contradiction with the wait's contract rejects the caller's answer.
func TestAnswerFailureRejectsOnlyContractContradictions(t *testing.T) {
	if err := answerFailure(errors.New("connection refused")); !errors.Is(err, errUnavailable) {
		t.Fatalf("store failure = %v, want unavailable", err)
	}
	for _, contradiction := range []error{run.ErrSignalConflict, run.ErrSignalRejected, run.ErrInvalidPayload} {
		if err := answerFailure(contradiction); !errors.Is(err, errArguments) {
			t.Fatalf("%v = %v, want invalid arguments", contradiction, err)
		}
	}
	if err := answerFailure(context.Canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
}
