// Package panicinfo bounds retained diagnostics for observational callbacks.
package panicinfo

import (
	"fmt"
	"runtime"
	"strings"
	"unicode/utf8"
)

const (
	MaxMessageBytes = 4 << 10
	MaxStackBytes   = 64 << 10
)

// Capture must run in the recovering goroutine. Cloning a truncated message
// prevents the diagnostic from retaining the full panic value's allocation.
func Capture(value any) (message, stack string) {
	message = strings.ToValidUTF8(fmt.Sprint(value), "\ufffd")
	if len(message) > MaxMessageBytes {
		message = message[:MaxMessageBytes]
		for !utf8.ValidString(message) {
			message = message[:len(message)-1]
		}
		message = strings.Clone(message)
	}
	buffer := make([]byte, MaxStackBytes)
	size := runtime.Stack(buffer, false)
	return message, string(buffer[:size])
}
