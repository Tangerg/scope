package trajectory

import (
	"testing"

	"github.com/Tangerg/scope/core/chat"
)

func TestCanonicalArgumentsPreservesNumbersAndNormalizesStrings(t *testing.T) {
	for _, raw := range []string{
		` { "z": 1e400, "n": 9007199254740993, "label": "\u003cvalue\u003e" } `,
		`{"label":"<value>","n":9007199254740993,"z":1e400}`,
	} {
		got, err := canonicalArguments(raw)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != `{"label":"<value>","n":9007199254740993,"z":1e400}` {
			t.Fatalf("canonical arguments = %s", got)
		}
	}
	for _, raw := range []string{"", " \n\t", "{}", " { } "} {
		got, err := canonicalArguments(raw)
		if err != nil || string(got) != "{}" {
			t.Fatalf("arguments %q = %s, %v; want the empty object", raw, got, err)
		}
	}
}

// One provider sends blank arguments for a parameterless call and another
// sends {}; both are the same call, so one expectation must match either.
func TestToolExpectationTreatsBlankArgumentsAsTheEmptyObject(t *testing.T) {
	for _, expected := range []ToolArguments{"", "{}"} {
		expectation := ToolExpectation{Name: "clock", Arguments: &expected}
		for _, actual := range []string{"", "{}"} {
			matched, err := expectation.matches(ToolCall{Call: chat.ToolCall{ID: "call", Name: "clock", Arguments: actual}})
			if err != nil || !matched {
				t.Fatalf("expected %q against actual %q: matched=%v err=%v", expected, actual, matched, err)
			}
		}
	}
}
