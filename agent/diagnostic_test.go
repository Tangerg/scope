package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizedDiagnosticsSurviveFailurePersistence(t *testing.T) {
	for _, test := range []struct {
		name, input, want string
	}{
		{"empty", "", "operation failed"},
		{"whitespace", " \n\t", "operation failed"},
		{"trimmed", " \nfailed\t", "failed"},
		{"invalid UTF-8", "bad\xfftext", "bad\ufffdtext"},
		{"exact limit", strings.Repeat("x", MaxDiagnosticBytes), strings.Repeat("x", MaxDiagnosticBytes)},
		{"split rune", strings.Repeat("x", MaxDiagnosticBytes-1) + "界", strings.Repeat("x", MaxDiagnosticBytes-1)},
		{"trim after truncation", strings.Repeat("x", MaxDiagnosticBytes-1) + " z", strings.Repeat("x", MaxDiagnosticBytes-1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			message := NormalizeDiagnostic(test.input)
			if message != test.want || !ValidDiagnostic(message) || NormalizeDiagnostic(message) != message {
				t.Fatalf("diagnostic = %q, want %q", message, test.want)
			}
			failure, err := NewFailure(FailureKindExternal, "test.diagnostic", message)
			if err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(failure)
			if err != nil {
				t.Fatal(err)
			}
			var restored Failure
			if err := json.Unmarshal(data, &restored); err != nil || restored != failure {
				t.Fatalf("restored failure = %+v, error = %v", restored, err)
			}
		})
	}
}
