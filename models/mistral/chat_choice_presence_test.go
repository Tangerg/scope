package mistral_test

import (
	"errors"
	"fmt"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
)

func TestChatRequiresReportedStreamChoiceIndex(t *testing.T) {
	for _, sample := range []struct {
		name  string
		index string
		valid bool
	}{
		{"missing", "", false},
		{"null", `"index":null,`, false},
		{"zero", `"index":0,`, true},
		{"different choice", `"index":1,`, false},
	} {
		t.Run(sample.name, func(t *testing.T) {
			model := newIdentityChat(t, fmt.Sprintf(`{"id":"response-1","model":"mistral-actual","choices":[{%s"delta":{"content":"answer"},"finish_reason":"stop"}]}`, sample.index))
			request := &corechat.Request{Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hello"))}}
			response, callErr := model.Call(t.Context(), request)
			if sample.valid {
				if callErr != nil || response == nil || response.Text() != "answer" || response.Output.FinishReason != corechat.FinishReasonStop {
					t.Fatalf("Call = %#v, %v", response, callErr)
				}
			} else if response != nil || !errors.Is(callErr, corechat.ErrInvalidResponse) {
				t.Fatalf("Call = %#v, %v; want invalid response", response, callErr)
			}

			terminal := false
			var streamErr error
			for delta, err := range model.Stream(t.Context(), request) {
				if err != nil {
					streamErr = err
					break
				}
				terminal = terminal || delta.FinishReason != ""
			}
			if sample.valid {
				if streamErr != nil || !terminal {
					t.Fatalf("Stream terminal=%t error=%v", terminal, streamErr)
				}
			} else if terminal || !errors.Is(streamErr, corechat.ErrInvalidResponse) {
				t.Fatalf("Stream terminal=%t error=%v; want invalid response", terminal, streamErr)
			}
		})
	}
}
