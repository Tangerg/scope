package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestInitializationFailureDiagnosticsSurviveJSON(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    string
	}{
		{name: "split rune", message: strings.Repeat("a", 4094) + "界", want: strings.Repeat("a", 4094)},
		{name: "space at limit", message: strings.Repeat("a", 4095) + " z", want: strings.Repeat("a", 4095)},
		{name: "invalid encoding", message: "failure: \xff", want: "failure: \ufffd"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cause := errors.New(test.message)
			var failure Failure
			engine, err := NewEngine(EngineConfig{
				ProcessInitializationOutcomeAcknowledger: ProcessInitializationOutcomeAcknowledgerFunc(func(_ context.Context, outcome ProcessInitializationOutcome) error {
					var present bool
					failure, present = outcome.Failure()
					if !present {
						return errors.New("failed initialization outcome has no failure")
					}
					return nil
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
					t.Errorf("Close: %v", closeErr)
				}
			})
			input, err := EncodeInput(childTestInput{Mode: "leaf"})
			if err != nil {
				t.Fatal(err)
			}
			process, err := engine.Start(t.Context(), failingStartDeployment(t, cause), input)
			if process != nil || !errors.Is(err, cause) {
				t.Fatalf("Start process = %v, error = %v", process, err)
			}
			if failure.Kind() != FailureKindExecution || failure.Code() != "engine.process.start.failed" || failure.Message() != test.want {
				t.Errorf("failure kind/code = %s/%s, diagnostic matches = %t", failure.Kind(), failure.Code(), failure.Message() == test.want)
			}
			data, err := json.Marshal(failure)
			if err != nil {
				t.Fatal(err)
			}
			var restored Failure
			if err := json.Unmarshal(data, &restored); err != nil {
				t.Fatal(err)
			}
			if restored != failure {
				t.Fatal("failure changed during JSON round trip")
			}
		})
	}
}

func TestFailureRejectsInvalidUTF8(t *testing.T) {
	if _, err := NewFailure(FailureKindExecution, "step.failed", "failure: \xff"); !errors.Is(err, ErrInvalidFailure) {
		t.Fatalf("NewFailure = %v, want ErrInvalidFailure", err)
	}
}

func TestTerminationRestorationMatchesFailureKind(t *testing.T) {
	kinds := []FailureKind{FailureKindExecution, FailureKindContract, FailureKindExternal, FailureKindPanic}
	causes := []TerminationCause{TerminationCauseExecutionFailure, TerminationCauseContractFailure, TerminationCauseExternalFailure, TerminationCausePanic}
	for kindIndex, kind := range kinds {
		for causeIndex, cause := range causes {
			t.Run(string(kind)+"/"+string(cause), func(t *testing.T) {
				failure, err := NewFailure(kind, "step.failed", "failure")
				if err != nil {
					t.Fatal(err)
				}
				data, err := json.Marshal(terminationWire{Status: StatusFailed, Cause: cause, Reason: "failure", Failure: &failure})
				if err != nil {
					t.Fatal(err)
				}
				var restored Termination
				err = json.Unmarshal(data, &restored)
				if kindIndex == causeIndex {
					if err != nil || restored.Cause() != cause {
						t.Fatalf("restore matching failure = %v, cause = %s", err, restored.Cause())
					}
				} else if !errors.Is(err, errInvalidTermination) {
					t.Fatalf("restore contradictory failure = %v, want invalid termination", err)
				}
			})
		}
	}
}

func TestTerminationRestorationEnforcesReasonBounds(t *testing.T) {
	for name, reason := range map[string]string{
		"whitespace": " reason ",
		"too long":   strings.Repeat("a", 4097),
	} {
		t.Run(name, func(t *testing.T) {
			for _, wire := range []terminationWire{
				{Status: StatusCanceled, Cause: TerminationCauseHostCancellation, Reason: reason},
				{Status: StatusTimedOut, Cause: TerminationCauseProcessDeadline, Reason: reason},
				{Status: StatusKilled, Cause: TerminationCauseEngineKill, Reason: reason},
			} {
				data, err := json.Marshal(wire)
				if err != nil {
					t.Fatal(err)
				}
				var restored Termination
				if err := json.Unmarshal(data, &restored); !errors.Is(err, errInvalidTermination) {
					t.Errorf("restore %s = %v, want invalid termination", wire.Status, err)
				}
			}
		})
	}
}
