package protocol

import (
	"strings"
	"testing"

	"google.golang.org/genai"

	corechat "github.com/Tangerg/scope/core/chat"
)

// Gemini returns no candidates only when the prompt itself was refused, and it
// puts the reason in promptFeedback. Counting candidates first would report a
// documented refusal as a response-shape complaint.
func TestMapResponseReportsBlockedPrompt(t *testing.T) {
	t.Parallel()

	for _, sample := range []struct {
		name     string
		feedback *genai.GenerateContentResponsePromptFeedback
		want     string
	}{
		{
			name: "reason only",
			feedback: &genai.GenerateContentResponsePromptFeedback{
				BlockReason: genai.BlockedReasonSafety,
			},
			want: "google: prompt blocked (SAFETY)",
		},
		{
			name: "reason and message",
			feedback: &genai.GenerateContentResponsePromptFeedback{
				BlockReason:        genai.BlockedReasonProhibitedContent,
				BlockReasonMessage: "the prompt names a prohibited topic",
			},
			want: "google: prompt blocked (PROHIBITED_CONTENT): the prompt names a prohibited topic",
		},
	} {
		t.Run(sample.name, func(t *testing.T) {
			response := &genai.GenerateContentResponse{PromptFeedback: sample.feedback}

			_, err := newProtocolResponseMapper("google").mapResponse("model", response)
			if err == nil || err.Error() != sample.want {
				t.Fatalf("mapResponse() = %v, want %q", err, sample.want)
			}
			_, err = newProtocolResponseMapper("google").mapDelta("model", response)
			if err == nil || err.Error() != sample.want {
				t.Fatalf("mapDelta() = %v, want %q", err, sample.want)
			}
		})
	}
}

// An unset or unspecified block reason is not a refusal, so the candidate list
// still decides the outcome.
func TestMapResponseIgnoresUnspecifiedBlockReason(t *testing.T) {
	t.Parallel()

	for _, feedback := range []*genai.GenerateContentResponsePromptFeedback{
		nil,
		{},
		{BlockReason: genai.BlockedReasonUnspecified},
		{SafetyRatings: []*genai.SafetyRating{{Category: genai.HarmCategoryHarassment}}},
	} {
		response := &genai.GenerateContentResponse{PromptFeedback: feedback}
		_, err := newProtocolResponseMapper("google").mapResponse("model", response)
		if err == nil || strings.Contains(err.Error(), "prompt blocked") {
			t.Fatalf("mapResponse() = %v, want the candidate-count error", err)
		}
	}
}

// Recitation withholds the content by policy, so it belongs with the other
// policy outcomes rather than in the unclassified bucket.
func TestFinishReasonGroupsPolicyOutcomes(t *testing.T) {
	t.Parallel()

	for _, sample := range []struct {
		reason genai.FinishReason
		want   corechat.FinishReason
	}{
		{reason: genai.FinishReasonStop, want: corechat.FinishReasonStop},
		{reason: genai.FinishReasonMaxTokens, want: corechat.FinishReasonLength},
		{reason: genai.FinishReasonSafety, want: corechat.FinishReasonContentFilter},
		{reason: genai.FinishReasonBlocklist, want: corechat.FinishReasonContentFilter},
		{reason: genai.FinishReasonProhibitedContent, want: corechat.FinishReasonContentFilter},
		{reason: genai.FinishReasonSPII, want: corechat.FinishReasonContentFilter},
		{reason: genai.FinishReasonRecitation, want: corechat.FinishReasonContentFilter},
		{reason: genai.FinishReasonImageRecitation, want: corechat.FinishReasonContentFilter},
		{reason: genai.FinishReasonImageSafety, want: corechat.FinishReasonContentFilter},
		{reason: genai.FinishReasonMalformedFunctionCall, want: corechat.FinishReasonOther},
		{reason: genai.FinishReasonUnexpectedToolCall, want: corechat.FinishReasonOther},
		{reason: genai.FinishReasonTooManyToolCalls, want: corechat.FinishReasonOther},
		{reason: genai.FinishReasonLanguage, want: corechat.FinishReasonOther},
		{reason: genai.FinishReasonNoImage, want: corechat.FinishReasonOther},
	} {
		t.Run(string(sample.reason), func(t *testing.T) {
			if got := normalizeProtocolFinishReason(sample.reason, false); got != sample.want {
				t.Fatalf("normalizeProtocolFinishReason(%q) = %q, want %q",
					sample.reason, got, sample.want)
			}
		})
	}
}
