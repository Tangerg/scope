package panicinfo

import (
	"strings"
	"testing"
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
