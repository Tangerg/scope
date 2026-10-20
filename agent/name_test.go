package agent

import (
	jsonv2 "encoding/json/v2"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/jsonschema"
)

func TestQualifiedNameContract(t *testing.T) {
	schema, err := jsonschema.For[QualifiedName]()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		valid bool
	}{
		{"a", true}, {"action.prepare", true}, {"a1._-", true}, {strings.Repeat("a", maxQualifiedNameBytes), true},
		{"", false}, {"A", false}, {"1a", false}, {"a b", false}, {"ab!", false}, {"a\n", false}, {".a", false}, {strings.Repeat("a", maxQualifiedNameBytes+1), false},
	} {
		if got := ValidQualifiedName(tc.name); got != tc.valid {
			t.Errorf("ValidQualifiedName(%q)=%t, want %t", tc.name, got, tc.valid)
		}
		raw, encodeErr := jsonv2.Marshal(tc.name)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		if got := schema.Validate(raw) == nil; got != tc.valid {
			t.Errorf("name schema accepted %q=%t, want %t", tc.name, got, tc.valid)
		}
	}
}
