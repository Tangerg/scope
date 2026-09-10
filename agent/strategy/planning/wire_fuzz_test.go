package planning_test

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"

	"github.com/Tangerg/scope/agent/strategy/planning"
)

func FuzzWorldStateJSON(f *testing.F) {
	f.Add([]byte(`{"conditions":[]}`))
	f.Add([]byte(`{"conditions":[{"key":"world.ready","truth":"true"}]}`))
	f.Add([]byte(`{"conditions":[{"key":"world.ready","truth":"unknown"}]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		var state planning.WorldState
		if err := json.Unmarshal(data, &state); err != nil {
			return
		}
		if !state.Valid() {
			t.Fatal("decoded WorldState is invalid")
		}
		encoded, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		var restored planning.WorldState
		if err := json.Unmarshal(encoded, &restored); err != nil {
			t.Fatal(err)
		}
		if restored.Key() != state.Key() {
			t.Fatalf("round-trip key = %q, want %q", restored.Key(), state.Key())
		}
	})
}

func FuzzPlanJSON(f *testing.F) {
	f.Add([]byte(`{"actions":[],"total_cost":0}`))
	f.Add([]byte(`{"actions":["action.prepare","action.finish"],"total_cost":2}`))
	f.Add([]byte(`{"actions":[],"total_cost":1}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		var plan planning.Plan
		if err := json.Unmarshal(data, &plan); err != nil {
			return
		}
		if !plan.Valid() {
			t.Fatal("decoded Plan is invalid")
		}
		encoded, err := json.Marshal(plan)
		if err != nil {
			t.Fatal(err)
		}
		var restored planning.Plan
		if err := json.Unmarshal(encoded, &restored); err != nil {
			t.Fatal(err)
		}
		restoredJSON := mustJSON(t, restored)
		if !bytes.Equal(restoredJSON, encoded) {
			t.Fatalf("round-trip Plan differs: before=%s after=%s", encoded, restoredJSON)
		}
	})
}

func FuzzOutputJSON(f *testing.F) {
	f.Add([]byte(`{"outcome":"unreachable","world_state":{"conditions":[]},"attempts":[],"planning_passes":1}`))
	f.Add([]byte(`{"outcome":"stuck","world_state":{"conditions":[]},"attempts":[{"action_name":"action.finish","status":"failed","diagnostic":"refused"}],"planning_passes":1}`))
	f.Add([]byte(`{"outcome":"achieved","world_state":{"conditions":[]},"attempts":[],"planning_passes":0}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		var output planning.Output
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&output); err != nil {
			return
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF || output.Validate() != nil {
			return
		}
		encoded, err := json.Marshal(output)
		if err != nil {
			t.Fatal(err)
		}
		var restored planning.Output
		if err := json.Unmarshal(encoded, &restored); err != nil || restored.Validate() != nil {
			t.Fatalf("accepted Output did not round trip: %v", err)
		}
	})
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
