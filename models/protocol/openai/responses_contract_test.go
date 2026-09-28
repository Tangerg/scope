package openai_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/models/protocol/openai"
)

func TestResponsesToolsPreserveNonStrictSchema(t *testing.T) {
	for _, schema := range []string{
		`{"type":"object","properties":{"city":{"type":"string"}}}`,
		`{"type":"object","properties":{"address":{"type":"object","properties":{"city":{"type":"string"}}}},"required":["address"]}`,
		`{"type":"object","properties":{}}`,
	} {
		t.Run(schema, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				calls++
				var body struct {
					Tools []struct {
						Strict     *bool          `json:"strict"`
						Parameters map[string]any `json:"parameters"`
					} `json:"tools"`
				}
				if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
					t.Error(err)
				}
				if len(body.Tools) != 1 {
					t.Errorf("tools = %#v", body.Tools)
					return
				}
				tool := body.Tools[0]
				if tool.Strict == nil || *tool.Strict {
					t.Errorf("strict = %v, want explicit false", tool.Strict)
				}
				var expected map[string]any
				if err := jsonv2.Unmarshal([]byte(schema), &expected); err != nil {
					t.Error(err)
				}
				if !reflect.DeepEqual(tool.Parameters, expected) {
					t.Errorf("schema = %#v, want %#v", tool.Parameters, expected)
				}
				if strings.HasSuffix(request.URL.Path, "/input_tokens") {
					writer.Header().Set("Content-Type", "application/json")
					_, _ = writer.Write([]byte(`{"input_tokens":10}`))
					return
				}
				writeResponsesCompletion(writer)
			}))
			defer server.Close()
			model := newResponsesModel(t, server.URL, "test-model")
			request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("weather"))}, Tools: []chat.ToolDefinition{{Name: "weather", InputSchema: json.RawMessage(schema)}}}
			if _, err := model.Call(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			if count, err := model.CountInputTokens(t.Context(), request); err != nil || count != 10 {
				t.Fatalf("count = %d, %v", count, err)
			}
			if calls != 2 {
				t.Fatalf("calls = %d", calls)
			}
		})
	}
}

func TestResponsesRejectsUnsupportedAssistantMedia(t *testing.T) {
	value, err := media.NewURI("image/png", "https://example.com/image.png")
	if err != nil {
		t.Fatal(err)
	}
	model := newResponsesModel(t, "http://127.0.0.1:1", "test-model")
	request := &chat.Request{Messages: []chat.Message{
		chat.NewUserMessage(chat.NewTextPart("draw")),
		chat.NewAssistantMessage(chat.NewMediaPart(value)),
		chat.NewUserMessage(chat.NewTextPart("edit it")),
	}}
	if validateErr := request.Validate(); validateErr != nil {
		t.Fatal(validateErr)
	}
	_, err = model.Call(t.Context(), request)
	if err == nil || !strings.Contains(err.Error(), `messages[1]: parts[0]: unsupported assistant part "media"`) {
		t.Fatalf("Call = %v", err)
	}
	_, err = model.CountInputTokens(t.Context(), request)
	if err == nil || !strings.Contains(err.Error(), `messages[1]: parts[0]: unsupported assistant part "media"`) {
		t.Fatalf("CountInputTokens = %v", err)
	}
}

func TestResponsesRejectsCoreOwnedExtensions(t *testing.T) {
	for _, test := range []struct {
		name   string
		fields map[string]any
	}{
		{"model", map[string]any{"model": "native-model"}},
		{"input", map[string]any{"input": "native-input"}},
		{"tools", map[string]any{"tools": []any{}}},
		{"max_output_tokens", map[string]any{"max_output_tokens": 222}},
		{"temperature", map[string]any{"temperature": 0.25}},
		{"top_p", map[string]any{"top_p": 0.9}},
		{"stream", map[string]any{"stream": false}},
		{"tool_choice", map[string]any{"tool_choice": "auto"}},
		{"parallel_tool_calls", map[string]any{"parallel_tool_calls": false}},
		{"reasoning.effort", map[string]any{"reasoning": map[string]any{"effort": "high"}}},
		{"text.format", map[string]any{"text": map[string]any{"format": map[string]any{"type": "json_object"}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, defaults := range []bool{false, true} {
				options := chat.Options{}
				if err := options.Extensions.Set(openai.ResponsesRequestExtensionKey, test.fields); err != nil {
					t.Fatal(err)
				}
				config := openai.ResponsesConfig{APIKey: "test", BaseURL: "http://127.0.0.1:1", DefaultOptions: chat.Options{Model: "test-model"}}
				request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}}
				if defaults {
					config.DefaultOptions.Extensions = options.Extensions
				} else {
					request.Options = options
				}
				model, err := openai.NewResponses(t.Context(), config)
				if err != nil {
					t.Fatal(err)
				}
				_, err = model.Call(t.Context(), request)
				if err == nil || !strings.Contains(err.Error(), "owned by") {
					t.Fatalf("defaults=%v: Call = %v", defaults, err)
				}
				_, err = model.CountInputTokens(t.Context(), request)
				if err == nil || !strings.Contains(err.Error(), "owned by") {
					t.Fatalf("defaults=%v: CountInputTokens = %v", defaults, err)
				}
			}
		})
	}
}
