package mistral

import (
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
)

func TestFinishReasonCoversEveryDocumentedValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		raw  finishReason
		want corechat.FinishReason
	}{
		{raw: "", want: ""},
		{raw: finishReasonStop, want: corechat.FinishReasonStop},
		{raw: finishReasonLength, want: corechat.FinishReasonLength},
		{raw: finishReasonModelLength, want: corechat.FinishReasonLength},
		{raw: finishReasonToolCalls, want: corechat.FinishReasonToolCalls},
		{raw: finishReasonError, want: corechat.FinishReasonOther},
	}

	for _, test := range tests {
		t.Run(string(test.raw), func(t *testing.T) {
			if got := test.raw.normalized(); got != test.want {
				t.Fatalf("finishReason(%q).normalized() = %q, want %q", test.raw, got, test.want)
			}
		})
	}
}
