package openai_test

import (
	"errors"
	"strings"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/modeltest"
)

func TestChatStreamRejectsCoercedWireFields(t *testing.T) {
	for _, test := range []struct {
		name   string
		choice string
	}{
		{"numeric text", `{"index":0,"delta":{"content":42},"finish_reason":"stop"}`},
		{"boolean text", `{"index":0,"delta":{"content":true},"finish_reason":"stop"}`},
		{"object text", `{"index":0,"delta":{"content":{}},"finish_reason":"stop"}`},
		{"numeric tool ID", `{"index":0,"delta":{"tool_calls":[{"index":0,"id":42,"type":"function","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls"}`},
		{"numeric tool name", `{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call","type":"function","function":{"name":42,"arguments":"{}"}}]},"finish_reason":"tool_calls"}`},
		{"object tool arguments", `{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call","type":"function","function":{"name":"lookup","arguments":{}}}]},"finish_reason":"tool_calls"}`},
		{"boolean refusal", `{"index":0,"delta":{"refusal":true},"finish_reason":"stop"}`},
		{"numeric finish reason", `{"index":0,"delta":{"content":"answer"},"finish_reason":42}`},
		{"numeric role", `{"index":0,"delta":{"role":42,"content":"answer"},"finish_reason":"stop"}`},
		{"object audio transcript", `{"index":0,"delta":{"audio":{"id":"audio","transcript":{}}},"finish_reason":"stop"}`},
		{"numeric citation URL", `{"index":0,"delta":{"content":"answer","annotations":[{"type":"url_citation","url_citation":{"url":42,"title":"source","start_index":0,"end_index":1}}]},"finish_reason":"stop"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := newLifecycleStreamModel(t, []string{test.choice})
			response, err := model.Call(t.Context(), newToolStreamRequest(t))
			if response != nil || !errors.Is(err, corechat.ErrInvalidResponse) {
				t.Fatalf("coerced field reached Core: response=%#v error=%v", response, err)
			}
		})
	}
}

func TestChatStreamAdmitsNullableFieldsAndProviderExtensions(t *testing.T) {
	model := newLifecycleStreamModel(t, []string{
		`{"index":0,"delta":{"role":"assistant","content":null,"refusal":null,"vendor_state":{"text":42}},"finish_reason":null}`,
		`{"index":0,"delta":{"content":"answer"},"finish_reason":"stop"}`,
	})
	response, err := model.Call(t.Context(), newToolStreamRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	if response.Text() != "answer" {
		t.Fatalf("text=%q", response.Text())
	}
}

func TestResponsesStreamRejectsCoercedWireFields(t *testing.T) {
	type replacement struct {
		old string
		new string
	}
	for _, test := range []struct {
		name         string
		replacements []replacement
	}{
		{"valid fields", nil},
		{"numeric text", []replacement{{`"先查天气："`, `42`}}},
		{"boolean text", []replacement{{`"先查天气："`, `true`}}},
		{"object text", []replacement{{`"先查天气："`, `{}`}}},
		{"numeric terminal text", []replacement{{`"先查天气："`, `"42"`}, {`"text":"42"`, `"text":42`}}},
		{"numeric item ID", []replacement{{`"fc_1"`, `42`}}},
		{"numeric call ID", []replacement{{`"call_w"`, `42`}}},
		{"numeric tool name", []replacement{{`"name":"weather"`, `"name":42`}}},
		{"null item status", []replacement{{`"status":"completed","content"`, `"status":null,"content"`}}},
		{"object tool arguments", []replacement{{`"arguments":"{\"city\":\"BJ\"}"`, `"arguments":{"city":"BJ"}`}}},
		{"boolean argument fragment", []replacement{{`"{\"city\":\"BJ\"}"`, `"true"`}, {`"delta":"true"`, `"delta":true`}}},
		{"numeric reasoning summary", []replacement{{`"想想看"`, `"42"`}, {`"delta":"42"`, `"delta":42`}}},
		{"boolean reasoning summary", []replacement{{`"想想看"`, `"true"`}, {`"delta":"true"`, `"delta":true`}}},
		{"numeric encrypted reasoning", []replacement{{`"enc_xyz"`, `42`}}},
		{"numeric response ID", []replacement{{`"resp_x"`, `42`}}},
		{"boolean model", []replacement{{`"model":"gpt-5"`, `"model":true`}}},
		{"numeric citation URL", []replacement{{`"https://example.com/weather"`, `42`}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			events := responsesInterleavedEvents()
			for index := range events {
				for _, change := range test.replacements {
					events[index].Data = strings.ReplaceAll(events[index].Data, change.old, change.new)
				}
			}
			server := modeltest.AnthropicSSEServer(events)
			t.Cleanup(server.Close)
			response, err := newResponsesModel(t, server.URL, "gpt-5").Call(t.Context(), newResponsesChatRequest(t))
			if len(test.replacements) == 0 {
				if err != nil || response == nil {
					t.Fatalf("valid transcript: response=%#v error=%v", response, err)
				}
				return
			}
			if response != nil || !errors.Is(err, corechat.ErrInvalidResponse) {
				t.Fatalf("coerced field reached Core: response=%#v error=%v", response, err)
			}
		})
	}
}

func TestResponsesStreamAdmitsNullableFieldsAndProviderExtensions(t *testing.T) {
	server := modeltest.AnthropicSSEServer([]modeltest.AnthropicEvent{
		{Event: "response.created", Data: `{"type":"response.created","response":{"id":"resp","model":"model","created_at":1700000000.5,"status":"in_progress","error":null,"incomplete_details":null}}`},
		{Event: "response.future.delta", Data: `{"type":"response.future.delta","delta":{"text":42},"output_index":"future"}`},
		{Event: "response.output_text.delta", Data: `{"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"msg","delta":"answer","vendor_state":{"text":42}}`},
		{Event: "response.completed", Data: `{"type":"response.completed","response":{"id":"resp","model":"model","created_at":1700000000.5,"status":"completed","output":[{"type":"message","id":"msg","role":"assistant","status":"completed","phase":null,"content":[{"type":"output_text","text":"answer","annotations":[]}]},{"type":"tool_search_call","id":"search","status":"completed","arguments":{"query":"native object arguments"}}]}}`},
	})
	t.Cleanup(server.Close)
	response, err := newResponsesModel(t, server.URL, "model").Call(t.Context(), newResponsesChatRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	if response.Text() != "answer" {
		t.Fatalf("text=%q", response.Text())
	}
}

func TestResponsesStreamAdmitsNativeShellOutputDelta(t *testing.T) {
	output := `{"type":"shell_call_output","id":"shell-output","call_id":"shell-call","max_output_length":1024,"status":"completed","caller":null,"output":[{"stdout":"native output","stderr":"","outcome":{"type":"exit","exit_code":0}}]}`
	server := modeltest.AnthropicSSEServer([]modeltest.AnthropicEvent{
		{Event: "response.created", Data: `{"type":"response.created","response":{"id":"resp","model":"model","status":"in_progress"}}`},
		{Event: "response.output_item.added", Data: `{"type":"response.output_item.added","output_index":0,"item":{"type":"shell_call_output","id":"shell-output","call_id":"shell-call","max_output_length":1024,"status":"in_progress","caller":null,"output":[]}}`},
		{Event: "response.shell_call_output_content.delta", Data: `{"type":"response.shell_call_output_content.delta","item_id":"shell-output","output_index":0,"command_index":0,"sequence_number":2,"delta":{"stdout":"native output","stderr":""}}`},
		{Event: "response.shell_call_output_content.done", Data: `{"type":"response.shell_call_output_content.done","item_id":"shell-output","output_index":0,"command_index":0,"sequence_number":3,"output":[{"stdout":"native output","stderr":"","outcome":{"type":"exit","exit_code":0}}]}`},
		{Event: "response.output_item.done", Data: `{"type":"response.output_item.done","output_index":0,"item":` + output + `}`},
		{Event: "response.output_text.delta", Data: `{"type":"response.output_text.delta","item_id":"msg","output_index":1,"content_index":0,"delta":"answer"}`},
		{Event: "response.completed", Data: `{"type":"response.completed","response":{"id":"resp","model":"model","status":"completed","output":[` + output + `,{"type":"message","id":"msg","role":"assistant","status":"completed","content":[{"type":"output_text","text":"answer","annotations":[]}]}]}}`},
	})
	t.Cleanup(server.Close)
	response, err := newResponsesModel(t, server.URL, "model").Call(t.Context(), newResponsesChatRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	if response.Text() != "answer" {
		t.Fatalf("text=%q", response.Text())
	}
}
