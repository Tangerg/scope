package openai

import (
	"testing"

	"github.com/openai/openai-go/v3/responses"

	"github.com/Tangerg/scope/core/chat"
)

func TestResponsesExplicitFailurePreservesExecutionEvidence(t *testing.T) {
	for _, status := range []responses.ResponseStatus{responses.ResponseStatusFailed, responses.ResponseStatusCancelled} {
		t.Run(string(status), func(t *testing.T) {
			_, err := responsesTerminalDelta(&responses.Response{Status: status}, ResponsesResponseExtensionKey)
			if chat.ClassifyFailure(err) != chat.CallFailureFailed {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
