package openai_test

import (
	"errors"
	"fmt"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/modeltest"
)

func responsesToolAdded(index int, item string) modeltest.AnthropicEvent {
	return modeltest.AnthropicEvent{Event: "response.output_item.added", Data: fmt.Sprintf(`{"type":"response.output_item.added","output_index":%d,"item":%s}`, index, item)}
}

func responsesToolDone(item string) modeltest.AnthropicEvent {
	return modeltest.AnthropicEvent{Event: "response.output_item.done", Data: fmt.Sprintf(`{"type":"response.output_item.done","output_index":0,"item":%s}`, item)}
}

func responsesToolArguments(index int, itemID, fragment string) modeltest.AnthropicEvent {
	return modeltest.AnthropicEvent{Event: "response.function_call_arguments.delta", Data: fmt.Sprintf(`{"type":"response.function_call_arguments.delta","output_index":%d,"item_id":%q,"delta":%q}`, index, itemID, fragment)}
}

func TestResponsesToolStreamHasOneIdentityAndLifecycle(t *testing.T) {
	startItem := `{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"","status":"in_progress"}`
	doneItem := `{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{}","status":"completed"}`
	start := responsesToolAdded(0, startItem)
	arguments := responsesToolArguments(0, "fc_1", "{}")
	done := responsesToolDone(doneItem)
	for _, test := range []struct {
		name         string
		events       []modeltest.AnthropicEvent
		valid        bool
		incomplete   bool
		terminalItem string
	}{
		{name: "valid fragments", events: []modeltest.AnthropicEvent{start, arguments, done}, valid: true},
		{name: "initial arguments", events: []modeltest.AnthropicEvent{responsesToolAdded(0, doneItem), done}, valid: true},
		{name: "optional native item ID", events: []modeltest.AnthropicEvent{responsesToolAdded(0, `{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"}`), responsesToolDone(`{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"}`)}, valid: true},
		{name: "optional native item ID omitted on completion", events: []modeltest.AnthropicEvent{start, arguments, responsesToolDone(`{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"}`)}, terminalItem: `{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"}`, valid: true},
		{name: "partial tool truncation", events: []modeltest.AnthropicEvent{start, responsesToolArguments(0, "fc_1", "{")}, valid: true, incomplete: true},
		{name: "changed terminal identity", events: []modeltest.AnthropicEvent{start, arguments, done}, terminalItem: `{"type":"function_call","id":"fc_1","call_id":"call_2","name":"delete","arguments":"{}"}`},
		{name: "changed terminal native item ID", events: []modeltest.AnthropicEvent{start, arguments, done}, terminalItem: `{"type":"function_call","id":"fc_2","call_id":"call_1","name":"lookup","arguments":"{}"}`},
		{name: "negative output index", events: []modeltest.AnthropicEvent{responsesToolAdded(-1, startItem)}},
		{name: "duplicate start", events: []modeltest.AnthropicEvent{start, start, arguments, done}},
		{name: "changed identity", events: []modeltest.AnthropicEvent{start, responsesToolAdded(0, `{"type":"function_call","id":"fc_1","call_id":"call_2","name":"delete","arguments":""}`), arguments, done}},
		{name: "reused item ID", events: []modeltest.AnthropicEvent{start, responsesToolAdded(1, `{"type":"function_call","id":"fc_1","call_id":"call_2","name":"lookup","arguments":""}`), arguments, done}},
		{name: "reused call ID", events: []modeltest.AnthropicEvent{start, responsesToolAdded(1, `{"type":"function_call","id":"fc_2","call_id":"call_1","name":"lookup","arguments":""}`), arguments, done}},
		{name: "missing call ID", events: []modeltest.AnthropicEvent{responsesToolAdded(0, `{"type":"function_call","id":"fc_1","name":"lookup","arguments":"{}"}`), done}},
		{name: "arguments after done", events: []modeltest.AnthropicEvent{start, arguments, done, arguments}},
		{name: "duplicate done", events: []modeltest.AnthropicEvent{start, arguments, done, done}},
		{name: "unknown done", events: []modeltest.AnthropicEvent{done}},
		{name: "changed index", events: []modeltest.AnthropicEvent{start, responsesToolArguments(1, "fc_1", "{}"), done}},
		{name: "changed done identity", events: []modeltest.AnthropicEvent{start, arguments, responsesToolDone(`{"type":"function_call","id":"fc_1","call_id":"call_2","name":"delete","arguments":"{}"}`)}},
		{name: "changed done native item ID", events: []modeltest.AnthropicEvent{start, arguments, responsesToolDone(`{"type":"function_call","id":"fc_2","call_id":"call_1","name":"lookup","arguments":"{}"}`)}},
		{name: "missing done", events: []modeltest.AnthropicEvent{start, arguments}},
	} {
		t.Run(test.name, func(t *testing.T) {
			events := append([]modeltest.AnthropicEvent{{Event: "response.created", Data: `{"type":"response.created","response":{"id":"resp_1","model":"model","status":"in_progress"}}`}}, test.events...)
			status, detail, wantArguments := "completed", "", "{}"
			wantFinish := corechat.FinishReasonToolCalls
			if test.incomplete {
				status, detail, wantArguments = "incomplete", `"incomplete_details":{"reason":"max_output_tokens"},`, "{"
				wantFinish = corechat.FinishReasonLength
			}
			terminalItem := doneItem
			if test.incomplete {
				terminalItem = `{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{","status":"incomplete"}`
			}
			if test.terminalItem != "" {
				terminalItem = test.terminalItem
			}
			eventType := "response." + status
			events = append(events, modeltest.AnthropicEvent{Event: eventType, Data: fmt.Sprintf(`{"type":%q,"response":{"id":"resp_1","model":"model","status":%q,%s"output":[%s]}}`, eventType, status, detail, terminalItem)})
			server := modeltest.AnthropicSSEServer(events)
			t.Cleanup(server.Close)
			adapter := newResponsesModel(t, server.URL, "model")
			request := newToolStreamRequest(t)
			response, err := adapter.Call(t.Context(), request)
			if test.valid {
				if err != nil {
					t.Fatal(err)
				}
				parts := response.Output.Message.Parts
				if len(parts) != 1 || parts[0].ToolCall.ID != "call_1" || parts[0].ToolCall.Name != "lookup" || parts[0].ToolCall.Arguments != wantArguments || response.Output.FinishReason != wantFinish {
					t.Fatalf("native arguments or identity lost: %#v", parts)
				}
				return
			}
			if response != nil || !errors.Is(err, corechat.ErrInvalidResponse) {
				t.Fatalf("invalid tool lifecycle: %#v, %v", response, err)
			}
			var streamErr error
			for delta, err := range adapter.Stream(t.Context(), request) {
				if err != nil {
					streamErr = err
					break
				}
				if delta.FinishReason != "" {
					t.Fatal("invalid tool lifecycle completed successfully")
				}
			}
			if !errors.Is(streamErr, corechat.ErrInvalidResponse) {
				t.Fatalf("Stream error = %v", streamErr)
			}
		})
	}
}
