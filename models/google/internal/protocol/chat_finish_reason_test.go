package protocol

import (
	"testing"

	"google.golang.org/genai"

	corechat "github.com/Tangerg/scope/core/chat"
)

// Which Core reason each native finish reason becomes
// is a decision a caller acts on: content-filter tells them the policy withheld
// the output, length tells them to continue, other tells them nothing portable
// is known. The sibling adapters each pin their provider's enum this way; this
// mapping was the one left unpinned, so a bucket could be moved without any
// test noticing.
//
// Each expectation below is the SDK's own description of the value, which is
// generated from Google's API surface:
//
//   - LANGUAGE is "stopped because of using an unsupported language" — a
//     capability limit, not a policy withholding, so it is not content-filter.
//   - OTHER is "all other reasons" and IMAGE_OTHER is "for a reason not
//     otherwise specified", which is what Core's Other already means.
//   - The malformed, missing-signature and tool-count reasons are generation
//     faults.
//   - ESCALATION ("filtered by an escalation rule"), PUP_LIMITED_DISABLED
//     (stopped for Prohibited Use Policy violations) and MODEL_ARMOR ("blocked
//     by Model Armor") are policy stops, which Core's Other must not hide.
func TestNormalizeFinishReasonCoversTheSDKEnum(t *testing.T) {
	t.Parallel()

	tests := []struct {
		reason genai.FinishReason
		want   corechat.FinishReason
	}{
		{reason: "", want: ""},
		{reason: genai.FinishReasonUnspecified, want: ""},
		{reason: genai.FinishReasonStop, want: corechat.FinishReasonStop},
		{reason: genai.FinishReasonMaxTokens, want: corechat.FinishReasonLength},
		{reason: genai.FinishReasonContinuation, want: corechat.FinishReasonLength},

		{reason: genai.FinishReasonSafety, want: corechat.FinishReasonContentFilter},
		{reason: genai.FinishReasonRecitation, want: corechat.FinishReasonContentFilter},
		{reason: genai.FinishReasonBlocklist, want: corechat.FinishReasonContentFilter},
		{reason: genai.FinishReasonProhibitedContent, want: corechat.FinishReasonContentFilter},
		{reason: genai.FinishReasonSPII, want: corechat.FinishReasonContentFilter},
		{reason: genai.FinishReasonImageSafety, want: corechat.FinishReasonContentFilter},
		{reason: genai.FinishReasonImageProhibitedContent, want: corechat.FinishReasonContentFilter},
		{reason: genai.FinishReasonImageRecitation, want: corechat.FinishReasonContentFilter},
		{reason: genai.FinishReasonEscalation, want: corechat.FinishReasonContentFilter},
		{reason: genai.FinishReasonPupLimitedDisabled, want: corechat.FinishReasonContentFilter},
		{reason: genai.FinishReasonModelArmor, want: corechat.FinishReasonContentFilter},

		{reason: genai.FinishReasonLanguage, want: corechat.FinishReasonOther},
		{reason: genai.FinishReasonOther, want: corechat.FinishReasonOther},
		{reason: genai.FinishReasonImageOther, want: corechat.FinishReasonOther},
		{reason: genai.FinishReasonNoImage, want: corechat.FinishReasonOther},
		{reason: genai.FinishReasonMalformedFunctionCall, want: corechat.FinishReasonOther},
		{reason: genai.FinishReasonUnexpectedToolCall, want: corechat.FinishReasonOther},
		{reason: genai.FinishReasonTooManyToolCalls, want: corechat.FinishReasonOther},
		{reason: genai.FinishReasonMissingThoughtSignature, want: corechat.FinishReasonOther},
		{reason: genai.FinishReasonMalformedResponse, want: corechat.FinishReasonOther},
	}

	for _, test := range tests {
		t.Run(string(test.reason), func(t *testing.T) {
			t.Parallel()

			if got := normalizeProtocolFinishReason(test.reason, false); got != test.want {
				t.Fatalf("normalizeProtocolFinishReason(%q) = %q, want %q", test.reason, got, test.want)
			}
		})
	}
}

// STOP is the one reason whose meaning depends on what the candidate carried:
// Gemini reports a tool call as an ordinary stop, so the accumulated parts are
// what distinguish "the model answered" from "the model wants a tool run". A
// caller that reads Stop here hands the user a reply and never runs the tool.
func TestNormalizeFinishReasonReadsAToolCallOutOfStop(t *testing.T) {
	t.Parallel()

	if got := normalizeProtocolFinishReason(genai.FinishReasonStop, true); got != corechat.FinishReasonToolCalls {
		t.Fatalf("normalizeProtocolFinishReason(STOP, hasToolCalls) = %q, want %q", got, corechat.FinishReasonToolCalls)
	}
	// Only STOP is reinterpreted; a truncated output is still truncated even
	// when the fragment happens to contain a tool call.
	if got := normalizeProtocolFinishReason(genai.FinishReasonMaxTokens, true); got != corechat.FinishReasonLength {
		t.Fatalf("normalizeProtocolFinishReason(MAX_TOKENS, hasToolCalls) = %q, want %q", got, corechat.FinishReasonLength)
	}
}
