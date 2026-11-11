package team

import (
	"errors"
	"strings"
	"testing"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

// The coordinator's output schema is its only guidance, so it must reject an
// unknown mode and accept a completing Decision that omits the mode.
func TestDecisionSchemaPublishesTheDecidedModes(t *testing.T) {
	schema, err := jsonschema.For[Decision]()
	if err != nil {
		t.Fatal(err)
	}
	for _, valid := range []string{`{"mode":"continue","state":null}`, `{"mode":"wait","state":null}`, `{"state":null,"output":"done"}`} {
		if err := schema.Validate([]byte(valid)); err != nil {
			t.Fatalf("schema rejected %s: %v", valid, err)
		}
	}
	for _, invalid := range []string{`{"mode":"sprint","state":null}`, `{"mode":"","state":null,"output":"done"}`} {
		if err := schema.Validate([]byte(invalid)); err == nil {
			t.Fatalf("schema admitted %s", invalid)
		}
	}
}

// A control is validated by the same reason rule the Runtime applies to the
// cancellation it declares, so an admitted decision never fails at declaration.
func TestControlRequiresAReasonTheRuntimeCanCarry(t *testing.T) {
	task := require(run.ParseChildKey("task"))
	for _, reason := range []string{"", " padded ", strings.Repeat("x", 4097)} {
		if err := (Control{Task: task, CancelReason: &reason}).validate(); !errors.Is(err, ErrInvalidDecision) {
			t.Fatalf("reason of %d bytes admitted: %v", len(reason), err)
		}
	}
	reason := "superseded"
	if err := (Control{Task: task, CancelReason: &reason}).validate(); err != nil {
		t.Fatal(err)
	}
}
