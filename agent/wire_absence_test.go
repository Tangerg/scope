package agent_test

import (
	"encoding"
	"fmt"
	"testing"

	"github.com/Tangerg/scope/agent"
)

func TestAbsentFactsCannotBeEncodedAsDurableEvidence(t *testing.T) {
	for _, value := range []interface{ MarshalJSON() ([]byte, error) }{
		agent.Payload{}, agent.ExecutionState{}, agent.Signal{}, agent.SignalRequest{},
		agent.Effect{}, agent.Settlement{}, agent.Transition{}, agent.Failure{},
		agent.Descriptor{}, agent.DeploymentRef{}, agent.ChildStartResult{}, agent.ChildOutcome{},
		agent.Termination{}, agent.ProcessSnapshot{}, agent.TreeSnapshot{}, agent.Event{}, agent.Delta{},
	} {
		t.Run(fmt.Sprintf("%T", value), func(t *testing.T) {
			if encoded, err := value.MarshalJSON(); err == nil || len(encoded) != 0 {
				t.Fatalf("absent fact was encoded as evidence: %s, %v", encoded, err)
			}
		})
	}
	for _, value := range []encoding.TextMarshaler{
		agent.EffectAttemptID{}, agent.TreeIncarnationID{}, agent.Digest{}, agent.Capability{},
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
		(*agent.Payload)(nil), (*agent.ExecutionState)(nil), (*agent.Signal)(nil), (*agent.SignalRequest)(nil),
		(*agent.Effect)(nil), (*agent.Settlement)(nil), (*agent.Transition)(nil), (*agent.Failure)(nil),
		(*agent.Descriptor)(nil), (*agent.DeploymentRef)(nil), (*agent.ChildSpec)(nil), (*agent.ChildStartResult)(nil),
		(*agent.ChildControlResult)(nil), (*agent.ChildOutcome)(nil), (*agent.Termination)(nil),
		(*agent.TreeSnapshot)(nil), (*agent.Event)(nil), (*agent.Delta)(nil),
		(*agent.Quota)(nil), (*agent.CapabilitySet)(nil), (*agent.Usage)(nil), (*agent.Budget)(nil), (*agent.TreeLimits)(nil),
	} {
		t.Run(fmt.Sprintf("%T", receiver), func(t *testing.T) {
			if err := receiver.UnmarshalJSON([]byte(`{}`)); err == nil {
				t.Fatal("missing receiver accepted a wire document")
			}
		})
	}
	for _, receiver := range []encoding.TextUnmarshaler{
		(*agent.ProcessID)(nil), (*agent.SignalID)(nil), (*agent.WaitID)(nil), (*agent.EffectID)(nil),
		(*agent.WaitKey)(nil), (*agent.ChildKey)(nil), (*agent.EffectAttemptID)(nil),
		(*agent.TreeIncarnationID)(nil), (*agent.Digest)(nil), (*agent.Capability)(nil), (*agent.Status)(nil),
	} {
		t.Run(fmt.Sprintf("%T", receiver), func(t *testing.T) {
			if err := receiver.UnmarshalText([]byte("candidate")); err == nil {
				t.Fatal("missing receiver accepted a text identity")
			}
		})
	}
}
