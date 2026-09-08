package trajectory

import "testing"

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
	for _, raw := range []string{"", " \n\t"} {
		got, err := canonicalArguments(raw)
		if err != nil || got != nil {
			t.Fatalf("empty arguments = %s, %v", got, err)
		}
	}
}
