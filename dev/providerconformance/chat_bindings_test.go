package providerconformance_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	jsonv2 "encoding/json/v2"

	"github.com/Tangerg/scope/dev/providerconformance/internal/chatcheck"
)

func TestChatBindings(t *testing.T) {
	for _, entry := range chatcheck.Plan() {
		t.Run(entry.ID, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				body, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, 1<<20))
				if err != nil {
					t.Error(err)
					return
				}
				assertBindingTokenLimit(t, entry.ID, body)
				text, tool := "SCOPE_OK", false
				switch {
				case strings.Contains(string(body), "SCOPE_REPLAY_831"):
					text = "SCOPE_REPLAY_831"
				case strings.Contains(string(body), "scope_probe"):
					tool = true
				case strings.Contains(string(body), "Return JSON"):
					assertBindingSchema(t, entry.ID, body)
					text = `{"answer":"scope"}`
				}
				writeChatBindingFixture(writer, entry.ID, text, tool)
			}))
			t.Cleanup(server.Close)
			entry.BaseURL = server.URL
			for _, scenario := range chatcheck.LiveCases(entry.ID) {
				t.Run(scenario.Semantics, func(t *testing.T) { runChatScenario(t, entry, scenario.Semantics, "test-key", "test-model") })
			}
		})
	}
}

func writeChatBindingFixture(writer http.ResponseWriter, entry, text string, tool bool) {
	writer.Header().Set("Content-Type", "text/event-stream")
	encoded, _ := jsonv2.Marshal(text)
	arguments := `{"nonce":"scope_nonce_719"}`
	encodedArguments, _ := jsonv2.Marshal(arguments)
	switch entry {
	case "openai-chat":
		delta, finish := `{"content":`+string(encoded)+`}`, "stop"
		if tool {
			delta = `{"tool_calls":[{"index":0,"id":"call_test","type":"function","function":{"name":"scope_probe","arguments":` + string(encodedArguments) + `}}]}`
			finish = "tool_calls"
		}
		fmt.Fprintf(writer, "data: {\"id\":\"fixture\",\"model\":\"test-model\",\"choices\":[{\"index\":0,\"delta\":%s}]}\n\n", delta)
		fmt.Fprintf(writer, "data: {\"id\":\"fixture\",\"model\":\"test-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"%s\"}],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":5,\"total_tokens\":12}}\n\ndata: [DONE]\n\n", finish)
	case "openai-responses":
		fmt.Fprint(writer, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"fixture\",\"model\":\"test-model\",\"status\":\"in_progress\"}}\n\n")
		item := `{"type":"message","id":"message_test","role":"assistant","status":"completed","content":[{"type":"output_text","text":` + string(encoded) + `,"annotations":[]}]}`
		if tool {
			item = `{"type":"function_call","id":"function_test","call_id":"call_test","name":"scope_probe","arguments":` + string(encodedArguments) + `,"status":"completed"}`
			fmt.Fprint(writer, "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"function_test\",\"call_id\":\"call_test\",\"name\":\"scope_probe\",\"arguments\":\"\"}}\n\n")
			fmt.Fprintf(writer, "event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":0,\"item_id\":\"function_test\",\"delta\":%s}\n\n", encodedArguments)
			fmt.Fprintf(writer, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":%s}\n\n", item)
		} else {
			fmt.Fprintf(writer, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":%s}\n\n", encoded)
		}
		fmt.Fprintf(writer, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"fixture\",\"model\":\"test-model\",\"status\":\"completed\",\"output\":[%s],\"usage\":{\"input_tokens\":7,\"output_tokens\":5,\"total_tokens\":12}}}\n\n", item)
	case "anthropic-messages":
		fmt.Fprint(writer, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"fixture\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"test-model\",\"content\":[],\"usage\":{\"input_tokens\":7,\"output_tokens\":0}}}\n\n")
		block, delta, finish := `{"type":"text","text":""}`, `{"type":"text_delta","text":`+string(encoded)+`}`, "end_turn"
		if tool {
			block = `{"type":"tool_use","id":"call_test","name":"scope_probe","input":{}}`
			delta = `{"type":"input_json_delta","partial_json":` + string(encodedArguments) + `}`
			finish = "tool_use"
		}
		fmt.Fprintf(writer, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":%s}\n\n", block)
		fmt.Fprintf(writer, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":%s}\n\n", delta)
		fmt.Fprint(writer, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		fmt.Fprintf(writer, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"%s\"},\"usage\":{\"output_tokens\":5}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", finish)
	case "google-generate-content":
		part := `{"text":` + string(encoded) + `}`
		if tool {
			part = `{"functionCall":{"id":"call_test","name":"scope_probe","args":` + arguments + `}}`
		}
		fmt.Fprintf(writer, "data: {\"responseId\":\"fixture\",\"modelVersion\":\"test-model\",\"candidates\":[{\"index\":0,\"content\":{\"role\":\"model\",\"parts\":[%s]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":7,\"candidatesTokenCount\":5,\"totalTokenCount\":12}}\n\n", part)
	}
}

func assertBindingSchema(t *testing.T, entry string, body []byte) {
	t.Helper()
	var path []string
	switch entry {
	case "openai-chat":
		path = []string{"response_format", "json_schema", "schema"}
	case "openai-responses":
		path = []string{"text", "format", "schema"}
	case "anthropic-messages":
		path = []string{"output_config", "format", "schema"}
	case "google-generate-content":
		path = []string{"generationConfig", "responseJsonSchema"}
	default:
		t.Error("binding has no schema assertion")
		return
	}
	encoded := json.RawMessage(body)
	for _, name := range path {
		var object map[string]json.RawMessage
		if err := jsonv2.Unmarshal(encoded, &object); err != nil {
			t.Error(err)
			return
		}
		encoded = object[name]
		if len(encoded) == 0 {
			t.Errorf("native schema field %s missing", name)
			return
		}
	}
	var schema struct {
		Type       string `json:"type"`
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
		Required             []string `json:"required"`
		AdditionalProperties *bool    `json:"additionalProperties"`
	}
	if err := jsonv2.Unmarshal(encoded, &schema); err != nil {
		t.Error(err)
		return
	}
	if schema.Type != "object" || len(schema.Properties) != 1 || schema.Properties["answer"].Type != "string" || !reflect.DeepEqual(schema.Required, []string{"answer"}) || schema.AdditionalProperties == nil || *schema.AdditionalProperties {
		t.Errorf("native schema changed: %s", encoded)
	}
}

func assertBindingTokenLimit(t *testing.T, entry string, body []byte) {
	t.Helper()
	var path []string
	switch entry {
	case "openai-chat":
		path = []string{"max_completion_tokens"}
	case "openai-responses":
		path = []string{"max_output_tokens"}
	case "anthropic-messages":
		path = []string{"max_tokens"}
	case "google-generate-content":
		path = []string{"generationConfig", "maxOutputTokens"}
	default:
		t.Error("binding has no output limit assertion")
		return
	}
	encoded := json.RawMessage(body)
	for _, name := range path {
		var object map[string]json.RawMessage
		if err := jsonv2.Unmarshal(encoded, &object); err != nil {
			t.Error(err)
			return
		}
		encoded = object[name]
	}
	var limit int64
	if err := jsonv2.Unmarshal(encoded, &limit); err != nil || limit != 256 {
		t.Errorf("native output limit = %s; want 256", encoded)
	}
}
