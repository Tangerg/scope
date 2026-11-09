package anthropic

import (
	"encoding/json"
	"testing"
)

func TestOutputSchemaPointerResolutionRejectsInvalidTargets(t *testing.T) {
	document := json.RawMessage(`{"type":"string","$defs":{"variants":{"anyOf":[{"type":"integer"}]}},"a/b":{"type":"boolean"},"~1":{"type":"number"}}`)
	for _, sample := range []struct{ reference, kind string }{
		{"#", `"string"`}, {"#/$defs/variants/anyOf/0", `"integer"`}, {"#/a~1b", `"boolean"`}, {"#/~01", `"number"`},
	} {
		resolved, err := resolveOutputSchemaReference(document, sample.reference)
		if err != nil || string(resolved.Type) != sample.kind {
			t.Fatalf("reference=%q resolved=%v error=%v", sample.reference, resolved, err)
		}
	}
	for _, reference := range []string{"#/missing", "#/%", "#/%GG", "#/a~2b", "#/$defs/variants/anyOf/00", "#/$defs/variants/anyOf/+0", "#/$defs/variants/anyOf/-1", "#/$defs/variants/anyOf/1", "#/$defs/variants/anyOf/-", "https://example.test/schema#/$defs/value", "#anchor", "#/type/property"} {
		if resolved, err := resolveOutputSchemaReference(document, reference); err == nil || resolved != nil {
			t.Fatalf("reference=%q resolved=%v error=%v", reference, resolved, err)
		}
	}
}
