package managed

import (
	"context"
	"errors"
	"fmt"
	"testing"

	sdk "github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/Tangerg/scope/agent/run"
)

// A storage outage while confirming an answer must stay retryable; only a
// contradiction with the wait's contract rejects the client's answer.
func TestAnswerFailureRejectsOnlyContractContradictions(t *testing.T) {
	storeDown := fmt.Errorf("load tree: %w", errors.New("connection refused"))
	if err := answerFailure(storeDown); !errors.Is(err, sdk.ErrInternalError) {
		t.Fatalf("store failure = %v, want internal error", err)
	}
	for _, contradiction := range []error{run.ErrSignalConflict, run.ErrSignalRejected, run.ErrInvalidPayload} {
		if err := answerFailure(contradiction); !errors.Is(err, sdk.ErrInvalidRequest) {
			t.Fatalf("%v = %v, want invalid request", contradiction, err)
		}
	}
	if err := answerFailure(context.DeadlineExceeded); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline = %v", err)
	}
}
