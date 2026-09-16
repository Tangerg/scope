package agent

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestTransitionConstructorsEnforceOwnedFields(t *testing.T) {
	effect, err := NewDispatcherEffect(json.RawMessage(`{"request":"model"}`))
	if err != nil {
		t.Fatal(err)
	}
	transition, err := Continue(2, effect)
	if err != nil {
		t.Fatal(err)
	}
	effects := transition.Effects()
	effects[0].payload[0] = '['
	if transition.Kind() != TransitionKindContinue || transition.ConsumedSignals() != 2 || string(transition.Effects()[0].Payload()) != `{"request":"model"}` {
		t.Fatalf("Continue transition mutated: %+v", transition)
	}

	waitID, _ := ParseWaitID("wait:1")
	if wait, err := Wait(1, waitID); err != nil || wait.Kind() != TransitionKindWait {
		t.Fatalf("Wait() = %+v, %v", wait, err)
	}
	output, _ := ParsePayload(json.RawMessage(`{"answer":42}`))
	if complete, err := Complete(0, output); err != nil || complete.Kind() != TransitionKindComplete {
		t.Fatalf("Complete() = %+v, %v", complete, err)
	}
	failure, _ := NewFailure(FailureKindExternal, "provider.unavailable", "Provider is unavailable.")
	if failed, err := Fail(0, failure); err != nil || failed.Kind() != TransitionKindFail {
		t.Fatalf("Fail() = %+v, %v", failed, err)
	}
}

func TestTransitionStrictUnionJSON(t *testing.T) {
	waitID, _ := ParseWaitID("wait:1")
	wait, err := Wait(3, waitID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(wait)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Transition
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if got, ok := decoded.WaitID(); !ok || got != waitID {
		t.Fatalf("decoded WaitID = %v, %t", got, ok)
	}

	invalid := []byte(`{"kind":"wait","consumed_signals":1,"wait_id":"wait:1","reason":"not allowed"}`)
	if err := json.Unmarshal(invalid, &decoded); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("invalid union error = %v, want ErrInvalidTransition", err)
	}
}

func TestFailureStrictRoundTrip(t *testing.T) {
	failure, err := NewFailure(FailureKindContract, "output.schema", "Output did not match the Descriptor schema.")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(failure)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Failure
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != failure {
		t.Fatalf("decoded Failure = %+v, want %+v", decoded, failure)
	}
	if _, err := NewFailure(FailureKindInvalid, "failure", "message"); !errors.Is(err, ErrInvalidFailure) {
		t.Fatalf("invalid failure error = %v, want ErrInvalidFailure", err)
	}
}

func FuzzTransitionJSONRoundTrip(f *testing.F) {
	f.Add([]byte(`{"kind":"continue","consumed_signals":1,"effects":[{"target":"dispatcher","payload":{"operation":"model"}}]}`))
	f.Add([]byte(`{"kind":"continue","consumed_signals":0,"effects":[{"target":"framework","payload":{"operation":"wait","key":"approval","signal_payload":{"kind":"wait_id"}}}]}`))
	f.Add([]byte(`{"kind":"wait","consumed_signals":1,"wait_id":"wait:1"}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		var transition Transition
		if err := json.Unmarshal(data, &transition); err != nil {
			return
		}
		encoded, err := json.Marshal(transition)
		if err != nil {
			t.Fatal(err)
		}
		var decoded Transition
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.Kind() != transition.Kind() || decoded.ConsumedSignals() != transition.ConsumedSignals() || len(decoded.Effects()) != len(transition.Effects()) {
			t.Fatalf("round trip = %+v, want %+v", decoded, transition)
		}
	})
}
