package protocol_test

import (
	"errors"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/invocation"
)

func TestChatPromptRefusalEvidence(t *testing.T) {
	const blocked = `{"promptFeedback":{"blockReason":"SAFETY","blockReasonMessage":"blocked by policy"}}`
	for _, provider := range []string{"google", "vertexai"} {
		for _, test := range []struct {
			name   string
			chunks []string
			kind   invocation.FailureKind
		}{
			{name: "refused", chunks: []string{blocked}, kind: invocation.FailureFailed},
			{name: "future blocking reason", chunks: []string{`{"promptFeedback":{"blockReason":"FUTURE_POLICY"}}`}, kind: invocation.FailureFailed},
			{name: "metadata then refused", chunks: []string{`{"responseId":"response-1"}`, blocked}, kind: invocation.FailureFailed},
			{name: "unspecified", chunks: []string{`{"promptFeedback":{"blockReason":"BLOCKED_REASON_UNSPECIFIED"}}`}, kind: invocation.FailureUnknown},
			{name: "Gemini unspecified", chunks: []string{`{"promptFeedback":{"blockReason":"BLOCK_REASON_UNSPECIFIED"}}`}, kind: invocation.FailureUnknown},
			{name: "candidate then blocked", chunks: []string{`{"candidates":[{"index":0,"content":{"parts":[{"text":"answer"}]}}]}`, blocked}, kind: invocation.FailureUnknown},
			{name: "finished then blocked", chunks: []string{`{"candidates":[{"index":0,"content":{"parts":[{"text":"answer"}]},"finishReason":"STOP"}]}`, blocked}, kind: invocation.FailureUnknown},
			{name: "candidate with block", chunks: []string{`{"promptFeedback":{"blockReason":"SAFETY"},"candidates":[{"index":0,"content":{"parts":[{"text":"answer"}]},"finishReason":"STOP"}]}`}, kind: invocation.FailureUnknown},
			{name: "identity changed with block", chunks: []string{`{"responseId":"response-1"}`, `{"responseId":"response-2","promptFeedback":{"blockReason":"SAFETY"}}`}, kind: invocation.FailureUnknown},
		} {
			t.Run(provider+"/"+test.name, func(t *testing.T) {
				model := newNativeContractChat(t, provider, nativeChatChunks(test.chunks...))
				request := &corechat.Request{Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hello"))}}
				response, err := model.Call(t.Context(), request)
				if response != nil || invocation.ClassifyFailure(err) != test.kind {
					t.Fatalf("Call = %+v, %v; want %s", response, err, test.kind)
				}
				if test.kind == invocation.FailureUnknown && !errors.Is(err, corechat.ErrInvalidResponse) {
					t.Fatalf("contradictory stream error = %v", err)
				}
				var streamErr error
				for delta, err := range model.Stream(t.Context(), request) {
					if err != nil {
						streamErr = err
						break
					}
					if delta.FinishReason != "" {
						t.Fatal("refused or contradictory stream published completion")
					}
				}
				if invocation.ClassifyFailure(streamErr) != test.kind {
					t.Fatalf("Stream error = %v; want %s", streamErr, test.kind)
				}
			})
		}
	}
}
