package anthropic

import (
	"testing"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"

	corechat "github.com/Tangerg/scope/core/chat"
)

// Anthropic reports three stop reasons that leave the caller holding a
// half-finished output, and Core's FinishReasonLength groups by that state
// rather than by the cause. Two are truncations Anthropic tells clients to
// treat alike. The third is pause_turn, of which Anthropic says "we paused a
// long-running turn. You may provide the response back as-is in a subsequent
// request to let the model continue" — incomplete, with continuation as the
// remedy, which is the same position a truncation leaves the caller in.
//
// pause_turn used to reach Other, not by argument but because the default
// branch caught it and this table recorded the result. Other hides it from
// every caller that decides whether to continue by reading the finish reason,
// and the rest of a paused turn would be lost without a sound. The exact
// reason is on the output either way, under the native stop reason key.
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
		{reason: anthropicsdk.StopReasonPauseTurn, want: corechat.FinishReasonLength},
		{reason: "invented_by_a_future_model", want: corechat.FinishReasonOther},
	} {
		t.Run(string(sample.reason), func(t *testing.T) {
			if got := normalizeProtocolStopReason(sample.reason); got != sample.want {
				t.Fatalf("normalizeProtocolStopReason(%q) = %q, want %q",
					sample.reason, got, sample.want)
			}
		})
	}
}
