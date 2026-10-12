package planning_test

import (
	jsonv2 "encoding/json/v2"
	"slices"
	"testing"

	"github.com/Tangerg/scope/agent/strategy/planning"
)

// The JSON codec and the generated schema must admit exactly the same tokens:
// TruthUnknown is absence, never a serialized value, so every schema token must
// round-trip and no other token may decode.
func TestTruthWireContractMatchesSchema(t *testing.T) {
	schema := planning.TruthFalse.JSONSchemaEnum()
	if want := []string{"false", "true"}; !slices.Equal(schema, want) {
		t.Fatalf("JSONSchemaEnum() = %v, want %v", schema, want)
	}
	for _, token := range schema {
		wire := `"` + token + `"`
		var decoded planning.Truth
		if err := jsonv2.Unmarshal([]byte(wire), &decoded); err != nil {
			t.Fatalf("schema token %q rejected by decoder: %v", token, err)
		}
		encoded, err := jsonv2.Marshal(decoded)
		if err != nil {
			t.Fatalf("schema token %q rejected by encoder: %v", token, err)
		}
		if string(encoded) != wire {
			t.Fatalf("round-trip %q = %s, want %s", token, encoded, wire)
		}
	}
}

// TruthUnknown stays a valid in-memory three-valued state but has no wire form,
// so both directions of the codec must reject the "unknown" token.
func TestTruthUnknownHasNoWireForm(t *testing.T) {
	if !planning.TruthUnknown.Valid() {
		t.Fatal("TruthUnknown must remain a valid in-memory truth")
	}
	if got := planning.TruthUnknown.String(); got != "unknown" {
		t.Fatalf("TruthUnknown.String() = %q, want %q", got, "unknown")
	}
	if _, err := jsonv2.Marshal(planning.TruthUnknown); err == nil {
		t.Fatal("marshaling TruthUnknown must fail: unknown is absence, not a token")
	}
	var decoded planning.Truth
	if err := jsonv2.Unmarshal([]byte(`"unknown"`), &decoded); err == nil {
		t.Fatal("decoding the \"unknown\" token must fail")
	}
}

// A rejected token must not overwrite a receiver that already holds a known value.
func TestTruthDecodeFailurePreservesReceiver(t *testing.T) {
	decoded := planning.TruthTrue
	if err := jsonv2.Unmarshal([]byte(`"unknown"`), &decoded); err == nil {
		t.Fatal("decoding \"unknown\" must fail")
	}
	if decoded != planning.TruthTrue {
		t.Fatalf("receiver mutated to %q after a failed decode", decoded)
	}
}
