// Package panicinfo bounds retained diagnostics for observational callbacks.
package panicinfo

import (
	"fmt"
	"runtime"
	"strings"
)

const (
	MaxMessageBytes = 4 << 10
	MaxStackBytes   = 64 << 10
)

// Capture must run in the recovering goroutine. Cloning a truncated message
// prevents the diagnostic from retaining the full panic value's allocation.
func Capture(value any) (message, stack string) {
	message = fmt.Sprint(value)
	if len(message) > MaxMessageBytes {
		message = strings.Clone(message[:MaxMessageBytes])
	}
	buffer := make([]byte, MaxStackBytes)
	size := runtime.Stack(buffer, false)
	return message, string(buffer[:size])
}
