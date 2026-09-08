package bedrock

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	corechat "github.com/Tangerg/scope/core/chat"
)

// Converse reports two truncations. Classifying the context-window case as
// other would hide it from every caller that checks for a truncated answer.
func TestStopReasonClassifiesEveryTruncationAsLength(t *testing.T) {
	t.Parallel()

	for _, sample := range []struct {
		reason types.StopReason
		want   corechat.FinishReason
	}{
		{reason: types.StopReasonEndTurn, want: corechat.FinishReasonStop},
		{reason: types.StopReasonStopSequence, want: corechat.FinishReasonStop},
		{reason: types.StopReasonMaxTokens, want: corechat.FinishReasonLength},
		{reason: types.StopReasonModelContextWindowExceeded, want: corechat.FinishReasonLength},
		{reason: types.StopReasonToolUse, want: corechat.FinishReasonToolCalls},
		{reason: types.StopReasonContentFiltered, want: corechat.FinishReasonContentFilter},
		{reason: types.StopReasonGuardrailIntervened, want: corechat.FinishReasonContentFilter},
		{reason: types.StopReasonMalformedModelOutput, want: corechat.FinishReasonOther},
		{reason: types.StopReasonMalformedToolUse, want: corechat.FinishReasonOther},
	} {
		t.Run(string(sample.reason), func(t *testing.T) {
			if got := mapProtocolStopReason(sample.reason); got != sample.want {
				t.Fatalf("mapProtocolStopReason(%q) = %q, want %q",
					sample.reason, got, sample.want)
			}
		})
	}
}

// Every documented stopReason must land on a reason Core recognizes, so a
// value added to the enum cannot slip through as an empty finish reason.
func TestStopReasonCoversTheDocumentedEnum(t *testing.T) {
	t.Parallel()

	for _, reason := range types.StopReason("").Values() {
		if got := mapProtocolStopReason(reason); !got.Valid() {
			t.Fatalf("mapProtocolStopReason(%q) = %q, which Core rejects", reason, got)
		}
	}
}
