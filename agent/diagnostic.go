package agent

import (
	"strings"
	"unicode/utf8"
)

// MaxDiagnosticBytes bounds persistable failure messages and strategy diagnostics.
const MaxDiagnosticBytes = 4096

// ValidDiagnostic reports whether a diagnostic can be persisted without repair.
func ValidDiagnostic(message string) bool {
	return message != "" && len(message) <= MaxDiagnosticBytes &&
		utf8.ValidString(message) && strings.TrimSpace(message) == message
}

// NormalizeDiagnostic makes arbitrary external text non-empty, trimmed UTF-8
// within MaxDiagnosticBytes. Callers remain responsible for excluding secrets.
func NormalizeDiagnostic(message string) string {
	message = strings.TrimSpace(strings.ToValidUTF8(message, "\ufffd"))
	if len(message) > MaxDiagnosticBytes {
		message = message[:MaxDiagnosticBytes]
		for !utf8.ValidString(message) {
			message = message[:len(message)-1]
		}
		message = strings.TrimSpace(message)
	}
	if message == "" {
		return "operation failed"
	}
	return message
}
