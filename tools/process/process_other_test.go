//go:build !unix

package process_test

import (
	"errors"
	"testing"

	"github.com/Tangerg/scope/tools/process"
)

func TestUnsupportedPlatformRejectsConstruction(t *testing.T) {
	if _, err := process.New(t.Context(), process.Config{Argv: []string{"command"}, Directory: "."}); !errors.Is(err, process.ErrUnsupportedPlatform) {
		t.Fatalf("New = %v, want ErrUnsupportedPlatform", err)
	}
}
