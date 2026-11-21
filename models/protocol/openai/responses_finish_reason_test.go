package openai

import (
	"testing"

	"github.com/openai/openai-go/v3/responses"

	corechat "github.com/Tangerg/scope/core/chat"
)

func TestResponsesIncompleteReasonsNeverHideTruncation(t *testing.T) {
	for reason, want := range map[string]corechat.FinishReason{
		"max_output_tokens": corechat.FinishReasonLength,
		"max_messages":      corechat.FinishReasonLength,
		"content_filter":    corechat.FinishReasonContentFilter,
		"steered":           corechat.FinishReasonOther,
	} {
		response := &responses.Response{Status: responses.ResponseStatusIncomplete}
		response.IncompleteDetails.Reason = reason
		got, err := responsesFinishReason(response)
		if err != nil || got != want || responsesNativeFinishReason(response) != reason {
			t.Fatalf("incomplete %q = %q, %v; want %q", reason, got, err, want)
		}
	}
}
