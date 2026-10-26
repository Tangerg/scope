package openai_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/modeltest"
)

func TestResponsesReasoningRejectsContradictoryCompletion(t *testing.T) {
	item := `{"type":"reasoning","id":"rs-1","summary":[{"type":"summary_text","text":"observed"}],"encrypted_content":"signed","status":"completed"}`
	changed := `{"type":"reasoning","id":"rs-1","summary":[{"type":"summary_text","text":"changed"}],"encrypted_content":"signed","status":"completed"}`
	delta := modeltest.AnthropicEvent{Event: "response.reasoning_summary_text.delta", Data: `{"type":"response.reasoning_summary_text.delta","output_index":0,"item_id":"rs-1","summary_index":0,"delta":"observed"}`}
	done := func(value string) modeltest.AnthropicEvent {
		return modeltest.AnthropicEvent{Event: "response.output_item.done", Data: `{"type":"response.output_item.done","output_index":0,"item":` + value + `}`}
	}
	for _, test := range []struct {
		name   string
		events []modeltest.AnthropicEvent
		output string
		valid  bool
	}{
		{name: "matching completion", events: []modeltest.AnthropicEvent{delta, done(item)}, output: item, valid: true},
		{name: "item completion changes text", events: []modeltest.AnthropicEvent{delta, done(changed)}, output: changed},
		{name: "terminal changes text", events: []modeltest.AnthropicEvent{delta, done(item)}, output: changed},
		{name: "terminal omits reasoning", events: []modeltest.AnthropicEvent{delta, done(item)}},
		{name: "missing item completion", events: []modeltest.AnthropicEvent{delta}, output: item},
		{name: "duplicate item completion", events: []modeltest.AnthropicEvent{delta, done(item), done(item)}, output: item},
		{name: "delta after item completion", events: []modeltest.AnthropicEvent{delta, done(item), delta}, output: item},
		{name: "unseen terminal reasoning", output: item},
	} {
		t.Run(test.name, func(t *testing.T) {
			events := append([]modeltest.AnthropicEvent(nil), test.events...)
			events = append(events, modeltest.AnthropicEvent{Event: "response.completed", Data: fmt.Sprintf(`{"type":"response.completed","response":{"id":"response-1","model":"model","status":"completed","output":[%s]}}`, test.output)})
			server := modeltest.AnthropicSSEServer(events)
			t.Cleanup(server.Close)
			model := newResponsesModel(t, server.URL, "model")
			request := newResponsesChatRequest(t)
			response, err := model.Call(t.Context(), request)
			if test.valid {
				if err != nil || response == nil {
					t.Fatalf("Call = %v, %v", response, err)
				}
				return
			}
			if response != nil || !errors.Is(err, chat.ErrInvalidResponse) {
				t.Fatalf("Call = %v, %v; want ErrInvalidResponse", response, err)
			}
			var streamErr error
			for delta, err := range model.Stream(t.Context(), request) {
				if err != nil {
					streamErr = err
					break
				}
				if delta.FinishReason != "" {
					t.Fatal("contradictory reasoning published completion")
				}
			}
			if !errors.Is(streamErr, chat.ErrInvalidResponse) {
				t.Fatalf("Stream error = %v", streamErr)
			}
		})
	}
}
