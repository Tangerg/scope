package stepfail

import (
	"errors"
	"strings"
	"testing"

	"github.com/Tangerg/scope/agent/run"
)

func TestTransitionNormalizesDynamicDiagnostics(t *testing.T) {
	raw := "  " + strings.Repeat("x", run.MaxDiagnosticBytes+10) + "\n"
	transition, err := Transition(2, run.FailureKindExternal, "strategy.test.failed", raw)
	if err != nil {
		t.Fatal(err)
	}
	failure, failed := transition.Failure()
	if !failed || transition.Consumed() != 2 || failure.Code() != "strategy.test.failed" ||
		failure.Message() != run.NormalizeDiagnostic(raw) {
		t.Fatalf("transition = %+v, failure = %+v", transition, failure)
	}
}

func TestFailureRejectsAnInvalidClassification(t *testing.T) {
	if _, err := Failure(run.FailureKindInvalid, "strategy.test.failed", "failed"); !errors.Is(err, run.ErrInvalidFailure) {
		t.Fatalf("invalid kind error = %v", err)
	}
	if _, err := Transition(0, run.FailureKindExecution, "Not A Code", "failed"); !errors.Is(err, run.ErrInvalidFailure) {
		t.Fatalf("invalid code error = %v", err)
	}
}
