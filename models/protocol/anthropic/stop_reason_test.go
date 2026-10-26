package anthropic

import (
	"testing"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"

	corechat "github.com/Tangerg/scope/core/chat"
)

func TestStopReasonClassifiesEveryTruncationAsLength(t *testing.T) {
	t.Parallel()

	for _, sample := range []struct {
		reason anthropicsdk.StopReason
		want   corechat.FinishReason
	}{
		{reason: "", want: ""},
		{reason: anthropicsdk.StopReasonEndTurn, want: corechat.FinishReasonStop},
		{reason: anthropicsdk.StopReasonStopSequence, want: corechat.FinishReasonStop},
		{reason: anthropicsdk.StopReasonMaxTokens, want: corechat.FinishReasonLength},
		{reason: anthropicsdk.StopReasonModelContextWindowExceeded, want: corechat.FinishReasonLength},
		{reason: anthropicsdk.StopReasonToolUse, want: corechat.FinishReasonToolCalls},
		{reason: anthropicsdk.StopReasonRefusal, want: corechat.FinishReasonRefusal},
		{reason: "invented_by_a_future_model", want: corechat.FinishReasonOther},
	} {
		t.Run(string(sample.reason), func(t *testing.T) {
			if got, err := normalizeProtocolStopReason(sample.reason); err != nil || got != sample.want {
				t.Fatalf("normalizeProtocolStopReason(%q) = %q, %v; want %q",
					sample.reason, got, err, sample.want)
			}
		})
	}
}
