package openai_test

import (
	"errors"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/modeltest"
)

func TestResponsesStreamCompletionWaitsForNativeBody(t *testing.T) {
	completed := modeltest.AnthropicEvent{Event: "response.completed", Data: `{"type":"response.completed","response":{"id":"resp_1","model":"model","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"answer","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":2}}}`}
	for _, tc := range []struct {
		name            string
		tail            []modeltest.AnthropicEvent
		invalidResponse bool
	}{
		{name: "clean end"},
		{name: "late text", tail: []modeltest.AnthropicEvent{{Event: "response.output_text.delta", Data: `{"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"msg_1","delta":"late"}`}}, invalidResponse: true},
		{name: "duplicate completion", tail: []modeltest.AnthropicEvent{completed}, invalidResponse: true},
		{name: "native error", tail: []modeltest.AnthropicEvent{{Event: "error", Data: `{"type":"error","code":"tail_error","message":"late failure"}`}}},
		{name: "malformed frame", tail: []modeltest.AnthropicEvent{{Event: "response.output_text.delta", Data: `{`}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := []modeltest.AnthropicEvent{
				{Event: "response.created", Data: `{"type":"response.created","response":{"id":"resp_1","model":"model","status":"in_progress"}}`},
				{Event: "response.output_text.delta", Data: `{"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"msg_1","delta":"answer"}`},
				completed,
			}
			events = append(events, tc.tail...)
			server := modeltest.AnthropicSSEServer(events)
			t.Cleanup(server.Close)
			adapter := newResponsesModel(t, server.URL, "model")
			request := &corechat.Request{Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hello"))}}
			response, err := adapter.Call(t.Context(), request)
			if len(tc.tail) == 0 {
				if err != nil {
					t.Fatal(err)
				}
				parts := response.Output.Message.Parts
				if len(parts) != 1 || parts[0].Text != "answer" || response.Output.FinishReason != corechat.FinishReasonStop || response.Metadata.Usage == nil || response.Metadata.Usage.OutputTokens != 2 {
					t.Fatalf("Call lost content, completion, or usage: %#v", response)
				}
			} else if response != nil || err == nil || tc.invalidResponse && !errors.Is(err, corechat.ErrInvalidResponse) {
				t.Errorf("Call: response = %#v, error = %v; want failure", response, err)
			}
			var accumulator corechat.ResponseAccumulator
			var streamErr error
			for delta, err := range adapter.Stream(t.Context(), request) {
				if err != nil {
					streamErr = err
					break
				}
				if len(tc.tail) != 0 && delta.FinishReason != "" {
					t.Error("Stream published successful completion before a tail failure")
				}
				if err := accumulator.Add(delta); err != nil {
					t.Fatal(err)
				}
			}
			if len(tc.tail) != 0 {
				if streamErr == nil || tc.invalidResponse && !errors.Is(streamErr, corechat.ErrInvalidResponse) {
					t.Fatalf("Stream error = %v; want failure", streamErr)
				}
				if text := accumulator.Text(); text != "answer" {
					t.Fatalf("Stream text = %q, want partial answer", text)
				}
				return
			}
			if streamErr != nil {
				t.Fatal(streamErr)
			}
			response, err = accumulator.Response()
			if err != nil || response.Output.FinishReason != corechat.FinishReasonStop || response.Metadata.Usage == nil || response.Metadata.Usage.OutputTokens != 2 {
				t.Fatalf("Stream lost completion or usage: %#v, %v", response, err)
			}
		})
	}
}
