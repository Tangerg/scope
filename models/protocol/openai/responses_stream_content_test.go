package openai_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/modeltest"
)

func responsesTextEvent(outputIndex, contentIndex int, itemID, text string) modeltest.AnthropicEvent {
	return modeltest.AnthropicEvent{Event: "response.output_text.delta", Data: fmt.Sprintf(`{"type":"response.output_text.delta","output_index":%d,"content_index":%d,"item_id":%q,"delta":%q}`, outputIndex, contentIndex, itemID, text)}
}

func TestResponsesContentMustMatchTerminalProjection(t *testing.T) {
	answer := `{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"answer"}]}`
	for _, test := range []struct {
		name   string
		events []modeltest.AnthropicEvent
		output string
		valid  bool
	}{
		{name: "split text", events: []modeltest.AnthropicEvent{responsesTextEvent(0, 0, "msg_1", "an"), responsesTextEvent(0, 0, "msg_1", "swer")}, output: answer, valid: true},
		{name: "different text", events: []modeltest.AnthropicEvent{responsesTextEvent(0, 0, "msg_1", "wrong")}, output: answer},
		{name: "missing message", events: []modeltest.AnthropicEvent{responsesTextEvent(0, 0, "msg_1", "answer")}},
		{name: "changed item ID", events: []modeltest.AnthropicEvent{responsesTextEvent(0, 0, "other", "answer")}, output: answer},
		{name: "changed position", events: []modeltest.AnthropicEvent{responsesTextEvent(1, 0, "msg_1", "answer")}, output: answer},
		{name: "changed content position", events: []modeltest.AnthropicEvent{responsesTextEvent(0, 1, "msg_1", "answer")}, output: answer},
		{name: "changed identity between deltas", events: []modeltest.AnthropicEvent{responsesTextEvent(0, 0, "msg_1", "an"), responsesTextEvent(0, 0, "other", "swer")}, output: answer},
		{name: "reused message ID", events: []modeltest.AnthropicEvent{responsesTextEvent(0, 0, "msg_1", "an"), responsesTextEvent(1, 0, "msg_1", "swer")}, output: answer},
		{name: "unseen terminal text", output: answer},
		{name: "missing item ID", events: []modeltest.AnthropicEvent{responsesTextEvent(0, 0, "", "answer")}, output: answer},
		{name: "negative index", events: []modeltest.AnthropicEvent{responsesTextEvent(-1, 0, "msg_1", "answer")}, output: answer},
		{name: "missing indices", events: []modeltest.AnthropicEvent{{Event: "response.output_text.delta", Data: `{"type":"response.output_text.delta","item_id":"msg_1","delta":"answer"}`}}, output: answer},
		{name: "empty text", output: `{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":""}]}`, valid: true},
		{name: "native server tool", events: []modeltest.AnthropicEvent{responsesTextEvent(0, 0, "msg_1", "answer")}, output: answer + `,{"type":"web_search_call","id":"web_1","status":"completed"}`, valid: true},
		{name: "refusal", events: []modeltest.AnthropicEvent{{Event: "response.refusal.delta", Data: `{"type":"response.refusal.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"declined"}`}}, output: `{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"refusal","refusal":"declined"}]}`, valid: true},
		{name: "changed refusal", events: []modeltest.AnthropicEvent{{Event: "response.refusal.delta", Data: `{"type":"response.refusal.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"different"}`}}, output: `{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"refusal","refusal":"declined"}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			events := append([]modeltest.AnthropicEvent(nil), test.events...)
			events = append(events, modeltest.AnthropicEvent{Event: "response.completed", Data: `{"type":"response.completed","response":{"id":"resp_1","model":"model","status":"completed","output":[` + test.output + `]}}`})
			server := modeltest.AnthropicSSEServer(events)
			defer server.Close()
			model := newResponsesModel(t, server.URL, "model")
			request := newResponsesChatRequest(t)
			response, err := model.Call(t.Context(), request)
			if test.valid {
				if err != nil || response == nil {
					t.Fatalf("Call = %v, %v", response, err)
				}
			} else if response != nil || !errors.Is(err, chat.ErrInvalidResponse) || chat.ClassifyFailure(err) != chat.CallFailureUnknown {
				t.Fatalf("Call = %v, %v", response, err)
			}
			var accumulator chat.ResponseAccumulator
			var streamErr error
			for delta, err := range model.Stream(t.Context(), request) {
				if err != nil {
					streamErr = err
					break
				}
				if !test.valid && delta.FinishReason != "" {
					t.Fatal("contradictory response published successful completion")
				}
				if err := accumulator.Add(delta); err != nil {
					t.Fatal(err)
				}
			}
			if !test.valid {
				if !errors.Is(streamErr, chat.ErrInvalidResponse) {
					t.Fatalf("Stream error = %v", streamErr)
				}
				return
			}
			if streamErr != nil {
				t.Fatal(streamErr)
			}
			streamed, err := accumulator.Response()
			if err != nil || streamed.Output.Text() != response.Output.Text() || streamed.Output.FinishReason != response.Output.FinishReason {
				t.Fatalf("Call and Stream disagree: %v, %v, %v", response, streamed, err)
			}
		})
	}
}
