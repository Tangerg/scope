package agent

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestExecutionStateOwnsOpaquePayload(t *testing.T) {
	payload := json.RawMessage(` { "round": 2 } `)
	state, err := NewExecutionState("interaction", payload)
	if err != nil {
		t.Fatal(err)
	}
	payload[3] = 'x'
	copyOfPayload := state.Payload()
	copyOfPayload[0] = '['
	if got := string(state.Payload()); got != `{"round":2}` {
		t.Fatalf("ExecutionState.Payload() = %s", got)
	}
}

func TestExecutionStateStrictJSONRoundTrip(t *testing.T) {
	state, err := NewExecutionState("planning.goap", json.RawMessage(`{"phase":"observe"}`))
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ExecutionState
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Kind() != state.Kind() || string(decoded.Payload()) != string(state.Payload()) {
		t.Fatalf("decoded state = %+v, want %+v", decoded, state)
	}
	if err := json.Unmarshal([]byte(`{"kind":"planning","payload":{},"unknown":true}`), &decoded); !errors.Is(err, ErrInvalidExecutionState) {
		t.Fatalf("unknown field error = %v, want ErrInvalidExecutionState", err)
	}
}

func TestExecutionStateRejectsInvalidEnvelope(t *testing.T) {
	for _, test := range []struct {
		kind    string
		payload json.RawMessage
	}{
		{kind: "", payload: json.RawMessage(`{}`)},
		{kind: "interaction"},
	} {
		if _, err := NewExecutionState(test.kind, test.payload); !errors.Is(err, ErrInvalidExecutionState) {
			t.Fatalf("NewExecutionState(%q) error = %v, want ErrInvalidExecutionState", test.kind, err)
		}
	}
}

func FuzzExecutionStateJSONRoundTrip(f *testing.F) {
	f.Add([]byte(`{"kind":"interaction","payload":{"round":2}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		var state ExecutionState
		if err := json.Unmarshal(data, &state); err != nil {
			return
		}
		encoded, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		var decoded ExecutionState
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.Kind() != state.Kind() || string(decoded.Payload()) != string(state.Payload()) {
			t.Fatalf("round trip = %+v, want %+v", decoded, state)
		}
	})
}

func TestExecutionStateDecodeEnforcesKindAndPayload(t *testing.T) {
	type progress struct {
		Round int `json:"round"`
	}
	state, err := NewExecutionState("strategy.progress", []byte(`{"round":2}`))
	if err != nil {
		t.Fatal(err)
	}
	value, err := state.Decode[progress]("strategy.progress")
	if err != nil || value.Round != 2 {
		t.Fatalf("decode=%+v error=%v", value, err)
	}
	for _, test := range []struct {
		name, kind, payload string
	}{
		{name: "wrong kind", kind: "another.strategy", payload: `{"round":2}`},
		{name: "unknown member", kind: "strategy.progress", payload: `{"round":2,"extra":true}`},
		{name: "wrong type", kind: "strategy.progress", payload: `{"round":"two"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			state, err := NewExecutionState(test.kind, []byte(test.payload))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := state.Decode[progress]("strategy.progress"); !errors.Is(err, ErrInvalidExecutionState) {
				t.Fatalf("decode error=%v", err)
			}
		})
	}
	if _, err := (ExecutionState{}).Decode[progress]("strategy.progress"); !errors.Is(err, ErrInvalidExecutionState) {
		t.Fatalf("zero state error=%v", err)
	}
}
