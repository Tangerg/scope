package fs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home dir")
	}
	cases := map[string]string{
		"~":           home,
		"~/":          home,
		"~/Desktop/x": filepath.Join(home, "Desktop", "x"),
		"relative/x":  "relative/x",
		"/abs/x":      "/abs/x",
		"~user/x":     "~user/x",
		"a~b":         "a~b",
		"":            "",
	}
	for in, want := range cases {
		if got := expandHome(in); got != want {
			t.Errorf("expandHome(%q) = %q, want %q", in, got, want)
		}
	}
}
