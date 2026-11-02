package run

import (
	jsonv2 "encoding/json/v2"
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
			failure := newRuntimeFailure(FailureKindExecution, failureCodeRuntimeProcessStartFailed, cause)
			if failure.Kind() != FailureKindExecution || failure.Code() != "runtime.process.start.failed" || failure.Message() != test.want {
				t.Errorf("failure kind/code = %s/%s, diagnostic matches = %t", failure.Kind(), failure.Code(), failure.Message() == test.want)
			}
			data, err := jsonv2.Marshal(failure)
			if err != nil {
				t.Fatal(err)
			}
			var restored Failure
			if restoreErr := jsonv2.Unmarshal(data, &restored); restoreErr != nil {
				t.Fatal(restoreErr)
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

func TestFailureOwnsTerminationClassificationAndDiagnostic(t *testing.T) {
	for _, kind := range []FailureKind{FailureKindExecution, FailureKindContract, FailureKindExternal, FailureKindPanic} {
		t.Run(string(kind), func(t *testing.T) {
			failure, err := NewFailure(kind, "step.failed", "failure")
			if err != nil {
				t.Fatal(err)
			}
			data, err := jsonv2.Marshal(failure.termination())
			if err != nil {
				t.Fatal(err)
			}
			failureJSON, err := jsonv2.Marshal(failure)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != `{"failure":`+string(failureJSON)+`}` {
				t.Fatalf("termination duplicates failure facts: %s", data)
			}
			var restored Termination
			if restoreErr := jsonv2.Unmarshal(data, &restored); restoreErr != nil {
				t.Fatal(restoreErr)
			}
			if restored.Status() != StatusFailed || restored.Cause() != kind.terminationCause() || restored.Reason() != failure.Message() {
				t.Fatalf("restored failure changed: %+v", restored)
			}
			for _, wire := range []terminationWire{
				{Cause: kind.terminationCause(), Failure: &failure},
				{Reason: failure.Message(), Failure: &failure},
				{Cause: TerminationCauseCompletion, Failure: &failure},
			} {
				wireData, marshalErr := jsonv2.Marshal(wire)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				if restoreErr := jsonv2.Unmarshal(wireData, &restored); !errors.Is(restoreErr, errInvalidTermination) {
					t.Fatalf("accepted duplicate terminal facts %s: %v", wireData, restoreErr)
				}
			}
		})
	}
}

func TestTerminationRestorationEnforcesReasonBounds(t *testing.T) {
	for name, reason := range map[string]string{
		"whitespace": " reason ",
		"too long":   strings.Repeat("a", 4097),
	} {
		t.Run(name, func(t *testing.T) {
			for _, wire := range []terminationWire{
				{Cause: TerminationCauseHostCancellation, Reason: reason},
				{Cause: TerminationCauseRuntimeKill, Reason: reason},
			} {
				data, err := jsonv2.Marshal(wire)
				if err != nil {
					t.Fatal(err)
				}
				var restored Termination
				if err := jsonv2.Unmarshal(data, &restored); !errors.Is(err, errInvalidTermination) {
					t.Errorf("restore %s = %v, want invalid termination", wire.Cause, err)
				}
			}
		})
	}
}
