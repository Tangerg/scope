package anthropic

import (
	"errors"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
)

func TestMessagesRejectsCoercedWireFields(t *testing.T) {
	for _, test := range []struct {
		name  string
		start string
		delta string
		stop  string
	}{
		{"numeric text", `{"type":"text","text":""}`, `{"type":"text_delta","text":42}`, "end_turn"},
		{"boolean text", `{"type":"text","text":""}`, `{"type":"text_delta","text":true}`, "end_turn"},
		{"object text", `{"type":"text","text":""}`, `{"type":"text_delta","text":{}}`, "end_turn"},
		{"numeric tool ID", `{"type":"tool_use","id":42,"name":"lookup","input":{}}`, `{"type":"input_json_delta","partial_json":"{}"}`, "tool_use"},
		{"numeric tool name", `{"type":"tool_use","id":"call","name":42,"input":{}}`, `{"type":"input_json_delta","partial_json":"{}"}`, "tool_use"},
		{"object tool arguments", `{"type":"tool_use","id":"call","name":"lookup","input":{}}`, `{"type":"input_json_delta","partial_json":{}}`, "tool_use"},
		{"numeric thinking", `{"type":"thinking","thinking":"","signature":""}`, `{"type":"thinking_delta","thinking":42}`, "end_turn"},
		{"numeric signature", `{"type":"thinking","thinking":"reason","signature":""}`, `{"type":"signature_delta","signature":42}`, "end_turn"},
		{"numeric initial text", `{"type":"text","text":42}`, `{"type":"text_delta","text":"answer"}`, "end_turn"},
		{"boolean initial thinking", `{"type":"thinking","thinking":true,"signature":""}`, `{"type":"signature_delta","signature":"sig"}`, "end_turn"},
		{"object initial signature", `{"type":"thinking","thinking":"reason","signature":{}}`, `{"type":"signature_delta","signature":"sig"}`, "end_turn"},
		{"numeric redacted data", `{"type":"redacted_thinking","data":42}`, `{"type":"signature_delta","signature":"sig"}`, "end_turn"},
		{"numeric citation URL", `{"type":"text","text":"answer"}`, `{"type":"citations_delta","citation":{"type":"web_search_result_location","cited_text":"answer","url":42,"title":"source","encrypted_index":"idx"}}`, "end_turn"},
	} {
		t.Run(test.name, func(t *testing.T) {
			events := []string{
				`{"type":"message_start","message":{"id":"msg-test","type":"message","role":"assistant","model":"claude-test","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
				`{"type":"content_block_start","index":0,"content_block":` + test.start + `}`,
				`{"type":"content_block_delta","index":0,"delta":` + test.delta + `}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"message_delta","delta":{"stop_reason":"` + test.stop + `"},"usage":{"output_tokens":1}}`,
				`{"type":"message_stop"}`,
			}
			request := &corechat.Request{Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hello"))}}
			response, err := newLifecycleMessages(t, events).Call(t.Context(), request)
			if response != nil || !errors.Is(err, corechat.ErrInvalidResponse) {
				t.Fatalf("coerced field reached Core: response=%#v error=%v", response, err)
			}
		})
	}
}

func TestMessagesAdmitsNullableFieldsAndProviderExtensions(t *testing.T) {
	events := []string{
		`{"type":"message_start","message":{"id":"msg-test","type":"message","role":"assistant","model":"claude-test","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}`,
		`{"type":"future_delta","delta":{"text":42},"index":"future"}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"","vendor_state":{"text":42}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"answer","vendor_state":{"text":42}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"citations_delta","citation":{"type":"web_search_result_location","cited_text":"answer","url":"https://example.com/source","title":null,"encrypted_index":"idx"}}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":1}}`,
		`{"type":"message_stop"}`,
	}
	request := &corechat.Request{Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hello"))}}
	response, err := newLifecycleMessages(t, events).Call(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if response.Text() != "answer" {
		t.Fatalf("text=%q", response.Text())
	}
	parts := response.Output.Message.Parts
	if len(parts) != 1 || len(parts[0].Citations) != 1 || parts[0].Citations[0].Title != "" {
		t.Fatalf("nullable citation title lost its citation: %#v", parts)
	}
}
