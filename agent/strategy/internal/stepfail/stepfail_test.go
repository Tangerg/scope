package stepfail

import (
	"errors"
	"strings"
	"testing"

	"github.com/Tangerg/scope/agent"
)

func TestTransitionNormalizesDynamicDiagnostics(t *testing.T) {
	raw := "  " + strings.Repeat("x", agent.MaxDiagnosticBytes+10) + "\n"
	transition, err := Transition(2, agent.FailureKindExternal, "strategy.test.failed", raw)
	if err != nil {
		t.Fatal(err)
	}
	failure, failed := transition.Failure()
	if !failed || transition.ConsumedSignals() != 2 || failure.Code() != "strategy.test.failed" ||
		failure.Message() != agent.NormalizeDiagnostic(raw) {
		t.Fatalf("transition = %+v, failure = %+v", transition, failure)
	}
}

func TestFailureRejectsAnInvalidClassification(t *testing.T) {
	if _, err := Failure(agent.FailureKindInvalid, "strategy.test.failed", "failed"); !errors.Is(err, agent.ErrInvalidFailure) {
		t.Fatalf("invalid kind error = %v", err)
	}
	if _, err := Transition(0, agent.FailureKindExecution, "Not A Code", "failed"); !errors.Is(err, agent.ErrInvalidFailure) {
		t.Fatalf("invalid code error = %v", err)
	}
}
