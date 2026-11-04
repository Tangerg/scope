package openai_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/core/modeltest"
)

func responsesTextEvent(outputIndex, contentIndex int, itemID, text string) modeltest.AnthropicEvent {
	return modeltest.AnthropicEvent{Event: "response.output_text.delta", Data: fmt.Sprintf(`{"type":"response.output_text.delta","output_index":%d,"content_index":%d,"item_id":%q,"delta":%q}`, outputIndex, contentIndex, itemID, text)}
}

func responsesMessageDoneEvent(message string) modeltest.AnthropicEvent {
	return modeltest.AnthropicEvent{Event: "response.output_item.done", Data: `{"type":"response.output_item.done","output_index":0,"item":` + message + `}`}
}

func TestResponsesContentMustMatchTerminalProjection(t *testing.T) {
	answer := `{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"answer"}]}`
	completedAnswer := `{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"answer"}]}`
	answerWithEmptyPart := `{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"answer"},{"type":"output_text","text":""}]}`
	refusal := `{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"refusal","refusal":"declined"}]}`
	refusalDelta := modeltest.AnthropicEvent{Event: "response.refusal.delta", Data: `{"type":"response.refusal.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"declined"}`}
	textDelta := responsesTextEvent(0, 0, "msg_1", "answer")
	textDone := responsesMessageDoneEvent(answer)
	for _, test := range []struct {
		name   string
		events []modeltest.AnthropicEvent
		output string
		valid  bool
	}{
		{name: "split text", events: []modeltest.AnthropicEvent{responsesTextEvent(0, 0, "msg_1", "an"), responsesTextEvent(0, 0, "msg_1", "swer")}, output: answer, valid: true},
		{name: "completed message", events: []modeltest.AnthropicEvent{textDelta, responsesMessageDoneEvent(completedAnswer)}, output: completedAnswer, valid: true},
		{name: "completed refusal", events: []modeltest.AnthropicEvent{refusalDelta, responsesMessageDoneEvent(refusal)}, output: refusal, valid: true},
		{name: "text after done", events: []modeltest.AnthropicEvent{textDelta, textDone, responsesTextEvent(0, 0, "msg_1", "more")}, output: `{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"answermore"}]}`},
		{name: "empty text after done", events: []modeltest.AnthropicEvent{textDelta, textDone, responsesTextEvent(0, 0, "msg_1", "")}, output: answer},
		{name: "refusal after done", events: []modeltest.AnthropicEvent{refusalDelta, responsesMessageDoneEvent(refusal), refusalDelta}, output: refusal},
		{name: "duplicate done", events: []modeltest.AnthropicEvent{textDelta, textDone, textDone}, output: answer},
		{name: "added after done", events: []modeltest.AnthropicEvent{textDelta, textDone, {Event: "response.output_item.added", Data: `{"type":"response.output_item.added","output_index":0,"item":` + answer + `}`}}, output: answer},
		{name: "done omitted content", events: []modeltest.AnthropicEvent{textDelta, responsesMessageDoneEvent(`{"type":"message","id":"msg_1","role":"assistant","content":[]}`)}, output: answer},
		{name: "done changed content", events: []modeltest.AnthropicEvent{textDelta, responsesMessageDoneEvent(`{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"different"}]}`)}, output: answer},
		{name: "terminal added empty content after done", events: []modeltest.AnthropicEvent{textDelta, textDone}, output: answerWithEmptyPart},
		{name: "terminal removed empty content after done", events: []modeltest.AnthropicEvent{textDelta, responsesMessageDoneEvent(answerWithEmptyPart)}, output: answer},
		{name: "terminal changed empty content kind after done", events: []modeltest.AnthropicEvent{textDelta, responsesMessageDoneEvent(answerWithEmptyPart)}, output: `{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"answer"},{"type":"refusal","refusal":""}]}`},
		{name: "terminal changed completed message status", events: []modeltest.AnthropicEvent{textDelta, responsesMessageDoneEvent(completedAnswer)}, output: `{"type":"message","id":"msg_1","role":"assistant","status":"in_progress","content":[{"type":"output_text","text":"answer"}]}`},
		{name: "done message still in progress", events: []modeltest.AnthropicEvent{textDelta, responsesMessageDoneEvent(`{"type":"message","id":"msg_1","role":"assistant","status":"in_progress","content":[{"type":"output_text","text":"answer"}]}`)}, output: answer},
		{name: "done message unknown status", events: []modeltest.AnthropicEvent{textDelta, responsesMessageDoneEvent(`{"type":"message","id":"msg_1","role":"assistant","status":"new_status","content":[{"type":"output_text","text":"answer"}]}`)}, output: answer},
		{name: "text done after message done", events: []modeltest.AnthropicEvent{textDelta, textDone, {Event: "response.output_text.done", Data: `{"type":"response.output_text.done","output_index":0,"content_index":0,"item_id":"msg_1","text":"answer"}`}}, output: answer},
		{name: "refusal done after message done", events: []modeltest.AnthropicEvent{refusalDelta, responsesMessageDoneEvent(refusal), {Event: "response.refusal.done", Data: `{"type":"response.refusal.done","output_index":0,"content_index":0,"item_id":"msg_1","refusal":"declined"}`}}, output: refusal},
		{name: "content added after message done", events: []modeltest.AnthropicEvent{textDelta, textDone, {Event: "response.content_part.added", Data: `{"type":"response.content_part.added","output_index":0,"content_index":1,"item_id":"msg_1","part":{"type":"output_text","text":""}}`}}, output: answer},
		{name: "content done after message done", events: []modeltest.AnthropicEvent{textDelta, textDone, {Event: "response.content_part.done", Data: `{"type":"response.content_part.done","output_index":0,"content_index":0,"item_id":"msg_1","part":{"type":"output_text","text":"answer"}}`}}, output: answer},
		{name: "annotation after done", events: []modeltest.AnthropicEvent{textDelta, textDone, {Event: "response.output_text.annotation.added", Data: `{"type":"response.output_text.annotation.added","output_index":0,"content_index":0,"annotation_index":0,"item_id":"msg_1","annotation":{"type":"url_citation","start_index":0,"end_index":6,"title":"Source","url":"https://example.com"}}`}}, output: answer},
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
			} else if response != nil || !errors.Is(err, chat.ErrInvalidResponse) || invocation.ClassifyFailure(err) != invocation.FailureUnknown {
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

func TestResponsesDoneMessageCanBeIncomplete(t *testing.T) {
	message := `{"type":"message","id":"msg_1","role":"assistant","status":"incomplete","content":[{"type":"output_text","text":"partial"}]}`
	server := modeltest.AnthropicSSEServer([]modeltest.AnthropicEvent{
		responsesTextEvent(0, 0, "msg_1", "partial"),
		responsesMessageDoneEvent(message),
		{Event: "response.incomplete", Data: `{"type":"response.incomplete","response":{"id":"resp_1","model":"model","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[` + message + `]}}`},
	})
	defer server.Close()
	model := newResponsesModel(t, server.URL, "model")
	request := newResponsesChatRequest(t)
	response, err := model.Call(t.Context(), request)
	if err != nil || response == nil || response.Output.Text() != "partial" || response.Output.FinishReason != chat.FinishReasonLength {
		t.Fatalf("Call = %+v, %v", response, err)
	}
	streamed, err := chat.Collect(model.Stream(t.Context(), request))
	if err != nil || streamed == nil || streamed.Output.Text() != response.Output.Text() || streamed.Output.FinishReason != response.Output.FinishReason {
		t.Fatalf("Stream = %+v, %v", streamed, err)
	}
}
