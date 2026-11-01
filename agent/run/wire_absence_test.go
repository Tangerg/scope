package run_test

import (
	"encoding"
	"fmt"
	"testing"

	"github.com/Tangerg/scope/agent/run"
)

func TestAbsentFactsCannotBeEncodedAsDurableEvidence(t *testing.T) {
	for _, value := range []interface{ MarshalJSON() ([]byte, error) }{
		run.Payload{}, run.ExecutionState{}, run.Signal{}, run.SignalRequest{},
		run.Effect{}, run.Settlement{}, run.Transition{}, run.Failure{},
		run.Descriptor{}, run.ProgramRef{}, run.ChildStartResult{}, run.ChildOutcome{},
		run.Termination{}, run.ProcessSnapshot{}, run.TreeSnapshot{}, run.Event{}, run.Delta{},
	} {
		t.Run(fmt.Sprintf("%T", value), func(t *testing.T) {
			if encoded, err := value.MarshalJSON(); err == nil || len(encoded) != 0 {
				t.Fatalf("absent fact was encoded as evidence: %s, %v", encoded, err)
			}
		})
	}
	for _, value := range []encoding.TextMarshaler{
		run.EffectAttemptID{}, run.TreeIncarnationID{}, run.Digest{}, run.Capability{},
	} {
		t.Run(fmt.Sprintf("%T", value), func(t *testing.T) {
			if encoded, err := value.MarshalText(); err == nil || len(encoded) != 0 {
				t.Fatalf("absent identity was encoded: %q, %v", encoded, err)
			}
		})
	}
}

func TestWireDecodersRejectMissingReceivers(t *testing.T) {
	for _, receiver := range []interface{ UnmarshalJSON([]byte) error }{
		(*run.Payload)(nil), (*run.ExecutionState)(nil), (*run.Signal)(nil), (*run.SignalRequest)(nil),
		(*run.Effect)(nil), (*run.Settlement)(nil), (*run.Transition)(nil), (*run.Failure)(nil),
		(*run.Descriptor)(nil), (*run.ProgramRef)(nil), (*run.ChildSpec)(nil), (*run.ChildStartResult)(nil),
		(*run.ChildControlResult)(nil), (*run.ChildOutcome)(nil), (*run.Termination)(nil),
		(*run.TreeSnapshot)(nil), (*run.Event)(nil), (*run.Delta)(nil),
		(*run.Quota)(nil), (*run.CapabilitySet)(nil), (*run.Usage)(nil), (*run.Budget)(nil), (*run.TreeLimits)(nil),
	} {
		t.Run(fmt.Sprintf("%T", receiver), func(t *testing.T) {
			if err := receiver.UnmarshalJSON([]byte(`{}`)); err == nil {
				t.Fatal("missing receiver accepted a wire document")
			}
		})
	}
	for _, receiver := range []encoding.TextUnmarshaler{
		(*run.ProcessID)(nil), (*run.SignalID)(nil), (*run.WaitID)(nil), (*run.EffectID)(nil),
		(*run.WaitKey)(nil), (*run.ChildKey)(nil), (*run.EffectAttemptID)(nil),
		(*run.TreeIncarnationID)(nil), (*run.Digest)(nil), (*run.Capability)(nil), (*run.Status)(nil),
	} {
		t.Run(fmt.Sprintf("%T", receiver), func(t *testing.T) {
			if err := receiver.UnmarshalText([]byte("candidate")); err == nil {
				t.Fatal("missing receiver accepted a text identity")
			}
		})
	}
}
