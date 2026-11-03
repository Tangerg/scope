package tool_test

import (
	"testing"

	"github.com/Tangerg/scope/core/tool"
)

func TestRequestInputDenotesBlankArgumentsAsTheEmptyObject(t *testing.T) {
	for arguments, want := range map[string]string{
		"":          `{}`,
		" \n\t":     `{}`,
		`{"a":1}`:   `{"a":1}`,
		` {"a":1} `: ` {"a":1} `,
	} {
		if got := string((tool.Request{Name: "echo", Arguments: arguments}).Input()); got != want {
			t.Fatalf("Input(%q) = %q, want %q", arguments, got, want)
		}
	}
}
