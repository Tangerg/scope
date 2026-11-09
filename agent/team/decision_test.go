package team

import (
	"testing"

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
