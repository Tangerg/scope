package textread_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Tangerg/scope/tools/textread"
)

func TestLineFailureKeepsItsIdentityThroughWrapping(t *testing.T) {
	_, err := textread.Scan(t.Context(), strings.NewReader("ok\ntoo long"), textread.Options{
		InputBytes: 32, LineBytes: 3, OutputBytes: 32,
	})
	wrapped := fmt.Errorf("read document: %w", err)
	if !errors.Is(wrapped, textread.ErrLineTooLarge) || textread.LineNumber(wrapped) != 2 {
		t.Fatalf("line error = %v, line %d", wrapped, textread.LineNumber(wrapped))
	}
	if !strings.Contains(wrapped.Error(), textread.ErrLineTooLarge.Error()) {
		t.Fatalf("failure message lost its cause: %v", wrapped)
	}
	if textread.LineNumber(nil) != 0 || textread.LineNumber(textread.ErrInputTooLarge) != 0 {
		t.Fatal("non-line failure invented a line number")
	}
}
