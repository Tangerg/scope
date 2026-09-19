package panicinfo

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCaptureBoundsDiagnostic(t *testing.T) {
	for _, value := range []string{"failure", strings.Repeat("x", MaxMessageBytes*2)} {
		message, stack := Capture(value)
		if message != value[:min(len(value), MaxMessageBytes)] || len(stack) == 0 || len(stack) > MaxStackBytes {
			t.Fatalf("diagnostic sizes = %d, %d", len(message), len(stack))
		}
		if !strings.Contains(stack, "TestCaptureBoundsDiagnostic") {
			t.Fatal("diagnostic did not retain the calling goroutine")
		}
	}
}

func TestCapturePreservesUTF8AtByteBoundary(t *testing.T) {
	for _, value := range []string{strings.Repeat("€", 2000), "bad\xfftext"} {
		message, _ := Capture(value)
		if !utf8.ValidString(message) || len(message) > MaxMessageBytes {
			t.Fatalf("invalid retained diagnostic: %q", message)
		}
		if value[0] != 'b' && message != strings.Repeat("€", MaxMessageBytes/3) {
			t.Fatal("capture split or lost a complete rune")
		}
	}
}
