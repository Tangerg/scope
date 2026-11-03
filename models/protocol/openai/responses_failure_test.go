package openai

import (
	"testing"

	"github.com/openai/openai-go/v3/responses"

	"github.com/Tangerg/scope/core/invocation"
)

func TestResponsesExplicitFailurePreservesExecutionEvidence(t *testing.T) {
	for _, status := range []responses.ResponseStatus{responses.ResponseStatusFailed, responses.ResponseStatusCancelled} {
		t.Run(string(status), func(t *testing.T) {
			_, err := responsesTerminalDelta(&responses.Response{Status: status}, ResponsesResponseExtensionKey, streamIdentity{})
			if invocation.ClassifyFailure(err) != invocation.FailureFailed {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
