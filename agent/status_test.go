package agent

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestResolveTerminationPriorityMatrix(t *testing.T) {
	kill, _ := newKillIntent("operator requested kill")
	processDeadline, _ := newDeadlineIntent(deadlineOwnerProcess, "process deadline reached")
	parentDeadline, _ := newDeadlineIntent(deadlineOwnerParent, "parent deadline reached")
	hostDeadline, _ := newDeadlineIntent(deadlineOwnerHost, "host deadline reached")
	parentCancellation, _ := newCancellationIntent(cancellationOwnerParent, "parent canceled")
	hostCancellation, _ := newCancellationIntent(cancellationOwnerHost, "host canceled")
	executionFailure, _ := NewFailure(FailureKindExecution, "step.failed", "Step failed.")
	contractFailure, _ := NewFailure(FailureKindContract, "transition.invalid", "Transition is invalid.")
	externalFailure, _ := NewFailure(FailureKindExternal, "provider.failed", "Provider failed.")
	panicFailure, _ := NewFailure(FailureKindPanic, "step.panic", "Step panicked.")
	executionFailed, _ := failedOutcome(executionFailure)
	contractFailed, _ := failedOutcome(contractFailure)
	externalFailed, _ := failedOutcome(externalFailure)
	panicFailed, _ := failedOutcome(panicFailure)

	tests := []struct {
		name  string
		facts terminationFacts
		want  Status
		cause TerminationCause
	}{
		{name: "kill wins all", facts: terminationFacts{kill: kill, deadline: hostDeadline, cancellation: parentCancellation, outcome: externalFailed}, want: StatusKilled, cause: TerminationCauseEngineKill},
		{name: "process deadline", facts: terminationFacts{deadline: processDeadline, outcome: externalFailed}, want: StatusTimedOut, cause: TerminationCauseProcessDeadline},
		{name: "parent deadline", facts: terminationFacts{deadline: parentDeadline, outcome: externalFailed}, want: StatusTimedOut, cause: TerminationCauseParentDeadline},
		{name: "host deadline wins cancellation and failure", facts: terminationFacts{deadline: hostDeadline, cancellation: parentCancellation, outcome: externalFailed}, want: StatusTimedOut, cause: TerminationCauseHostDeadline},
		{name: "parent cancellation wins failure", facts: terminationFacts{cancellation: parentCancellation, outcome: externalFailed}, want: StatusCanceled, cause: TerminationCauseParentCancellation},
		{name: "host cancellation wins failure", facts: terminationFacts{cancellation: hostCancellation, outcome: externalFailed}, want: StatusCanceled, cause: TerminationCauseHostCancellation},
		{name: "execution failure", facts: terminationFacts{outcome: executionFailed}, want: StatusFailed, cause: TerminationCauseExecutionFailure},
		{name: "contract failure", facts: terminationFacts{outcome: contractFailed}, want: StatusFailed, cause: TerminationCauseContractFailure},
		{name: "external failure", facts: terminationFacts{outcome: externalFailed}, want: StatusFailed, cause: TerminationCauseExternalFailure},
		{name: "panic", facts: terminationFacts{outcome: panicFailed}, want: StatusFailed, cause: TerminationCausePanic},
		{name: "completion", facts: terminationFacts{outcome: completedOutcome()}, want: StatusCompleted, cause: TerminationCauseCompletion},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.facts.resolve()
			if err != nil {
				t.Fatal(err)
			}
			if !got.Valid() || got.Status() != test.want || got.Cause() != test.cause {
				t.Fatalf("terminationFacts.resolve() = status %s cause %s valid %t", got.Status(), got.Cause(), got.Valid())
			}
		})
	}
}

func TestStatusStrictJSONRoundTrip(t *testing.T) {
	statuses := []Status{
		StatusNotStarted, StatusRunning, StatusWaiting, StatusPaused,
		StatusCompleted, StatusFailed, StatusCanceled, StatusTimedOut, StatusKilled,
	}
	for _, status := range statuses {
		data, err := json.Marshal(status)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := string(data), `"`+string(status)+`"`; got != want {
			t.Fatalf("Status %q JSON = %s, want %s", status, got, want)
		}
		var decoded Status
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded != status {
			t.Fatalf("decoded Status = %s, want %s", decoded, status)
		}
	}
	if _, err := json.Marshal(StatusInvalid); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("marshal invalid Status error = %v, want ErrInvalidStatus", err)
	}
	var decoded Status
	priorSpelling := []byte{'"', 'c', 'a', 'n', 'c', 'e', 'l', 'l', 'e', 'd', '"'}
	if err := json.Unmarshal(priorSpelling, &decoded); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("prior Status spelling error = %v, want ErrInvalidStatus", err)
	}
}

func TestResolveTerminationRejectsMissingFacts(t *testing.T) {
	if _, err := (terminationFacts{}).resolve(); !errors.Is(err, errInvalidTermination) {
		t.Fatalf("empty terminationFacts.resolve() error = %v, want errInvalidTermination", err)
	}
	if _, err := newDeadlineIntent(deadlineOwnerInvalid, "deadline"); !errors.Is(err, errInvalidTermination) {
		t.Fatalf("newDeadlineIntent error = %v, want errInvalidTermination", err)
	}
}

func TestTerminationJSONRoundTripRejectsContradictoryState(t *testing.T) {
	failure, _ := NewFailure(FailureKindExternal, "dispatcher.failed", "dispatcher failed")
	termination := failure.termination()
	data, err := json.Marshal(termination)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Termination
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.Valid() || decoded.Status() != StatusFailed || decoded.Cause() != TerminationCauseExternalFailure {
		t.Fatalf("decoded termination=%+v", decoded)
	}
	contradictory := []byte(`{"status":"completed","cause":"external_failure","reason":"failed","failure":{"kind":"external","code":"dispatcher.failed","message":"failed"}}`)
	if err := json.Unmarshal(contradictory, &decoded); err == nil {
		t.Fatal("Termination accepted contradictory status and cause")
	}
}
