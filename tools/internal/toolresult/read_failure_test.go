package toolresult_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/tools/internal/toolresult"
)

func TestReadFailureDoesNotOverwriteCancellationOrExistingEvidence(t *testing.T) {
	failed, err := tool.NewFailure(tool.FailureConfig{Kind: tool.FailureKindRejected, Output: tool.NewTextOutput("denied")})
	if err != nil {
		t.Fatal(err)
	}
	observed, err := tool.NewCallError(tool.CallErrorConfig{Cause: errors.New("response lost"), Evidence: tool.NewTextOutput("partial")})
	if err != nil {
		t.Fatal(err)
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, observed, &tool.AuthorizationError{}, failed} {
		got := toolresult.ReadFailure("read", cause)
		if !errors.Is(got, cause) {
			t.Fatalf("diagnostic replaced: %v", got)
		}
		classification, definite := tool.ClassifyFailure(got)
		if errors.Is(cause, failed) {
			if !definite || classification != failed {
				t.Fatalf("rejection replaced: %v", got)
			}
		} else if definite {
			t.Fatalf("uncertainty became a completed read: %v", got)
		}
	}
	if toolresult.ReadFailure("read", nil) != nil {
		t.Fatal("nil became a failure")
	}
}

func TestReadFailureKeepsPrivateDiagnosticsOutsideTheModelResult(t *testing.T) {
	cause := errors.New("private filesystem path")
	got := toolresult.ReadFailure("read", cause)
	failure, definite := tool.ClassifyFailure(got)
	if !definite || failure.Kind() != tool.FailureKindFailed || !errors.Is(failure.Cause(), cause) {
		t.Fatalf("completed unsuccessful read lost its diagnostic: %v", got)
	}
	text, present := failure.Output().Text()
	if !present || text != "error: read failed" {
		t.Fatalf("diagnostic leaked into model output: %q, %v", text, present)
	}
}
