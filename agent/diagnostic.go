package agent

import (
	"strings"
	"unicode/utf8"
)

// MaxDiagnosticBytes bounds persistable failure messages and strategy diagnostics.
const MaxDiagnosticBytes = 4096

func ValidDiagnostic(message string) bool { return validBoundedText(message, MaxDiagnosticBytes) }

// validBoundedText is the one rule for persisted human-readable text:
// diagnostics, descriptions, and control reasons are non-empty, trimmed
// UTF-8 within their own byte limit.
func validBoundedText(text string, limit int) bool {
	return text != "" && len(text) <= limit && utf8.ValidString(text) && strings.TrimSpace(text) == text
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
