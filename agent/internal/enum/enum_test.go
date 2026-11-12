package enum_test

import (
	"testing"

	"github.com/Tangerg/scope/agent/internal/enum"
)

type color string

func (c color) Valid() bool { return c == "red" }

func TestStringHidesValuesOutsideTheVocabulary(t *testing.T) {
	if got := enum.String(color("red")); got != "red" {
		t.Fatalf("valid value = %q", got)
	}
	for _, value := range []color{"", "blue\nforged log line"} {
		if got := enum.String(value); got != "invalid" {
			t.Fatalf("String(%q) = %q", value, got)
		}
	}
}
