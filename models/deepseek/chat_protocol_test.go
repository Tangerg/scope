package deepseek_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/deepseek"
	"github.com/Tangerg/scope/models/protocol/openai"
)

func TestChat_ReasoningReplay(t *testing.T) {
	privateChain, err := openai.NewTextReasoningPart("deepseek", openai.TextReasoningContent, "private chain")
	if err != nil {
		t.Fatalf("NewTextReasoningPart: %v", err)
	}
	toolReasoning, err := openai.NewTextReasoningPart("deepseek", openai.TextReasoningContent, "need a tool")
	if err != nil {
		t.Fatalf("NewTextReasoningPart: %v", err)
	}
	tests := []struct {
		name              string
		messages          []corechat.Message
		wantReasoningWire bool
	}{
		{
			name: "ordinary previous turn omits reasoning",
			messages: []corechat.Message{
				corechat.NewUserMessage(corechat.NewTextPart("first")),
				corechat.NewAssistantMessage(
					privateChain,
					corechat.NewTextPart("first answer"),
				),
				corechat.NewUserMessage(corechat.NewTextPart("second")),
			},
		},
		{
			name: "tool turn replays reasoning",
			messages: []corechat.Message{
				corechat.NewUserMessage(corechat.NewTextPart("search")),
				corechat.NewAssistantMessage(
					toolReasoning,
					corechat.NewToolCallPart(corechat.ToolCall{ID: "call-1", Name: "search", Arguments: `{"q":"scope"}`}),
				),
				corechat.NewToolMessage(corechat.ToolResult{
					ID: "call-1", Name: "search", Output: corechat.NewTextToolOutput("found"),
				}),
			},
			wantReasoningWire: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var wireRequest struct {
				Messages []map[string]any `json:"messages"`
			}
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if err := json.NewDecoder(request.Body).Decode(&wireRequest); err != nil {
					t.Errorf("decode request: %v", err)
					http.Error(writer, "invalid request", http.StatusBadRequest)
					return
				}
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(`{"id":"chat-1","model":"deepseek-v4-flash","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
			}))
			t.Cleanup(server.Close)

			model, err := deepseek.NewChat(t.Context(), deepseek.ChatConfig{
				APIKey:  "test-key",
				BaseURL: server.URL,
				DefaultOptions: corechat.Options{
					Model: deepseek.ModelV4Flash,
				},
			})
			if err != nil {
				t.Fatalf("NewChat: %v", err)
			}
			if _, err := model.Call(t.Context(), &corechat.Request{Messages: test.messages}); err != nil {
				t.Fatalf("Call: %v", err)
			}

			assistant := findAssistantMessage(t, wireRequest.Messages)
			_, hasReasoning := assistant["reasoning_content"]
			if hasReasoning != test.wantReasoningWire {
				t.Fatalf("reasoning_content present = %v; want %v; message = %#v", hasReasoning, test.wantReasoningWire, assistant)
			}
		})
	}
}

func TestChatMapsOfficialRequestOptions(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"chat-1","model":"deepseek-v4-flash","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(server.Close)

	model, err := deepseek.NewChat(t.Context(), deepseek.ChatConfig{
		APIKey:         "test-key",
		BaseURL:        server.URL,
		DefaultOptions: corechat.Options{Model: deepseek.ModelV4Flash},
	})
	if err != nil {
		t.Fatalf("NewChat: %v", err)
	}
	logProbs := true
	topLogProbs := int64(5)
	request := &corechat.Request{
		Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("Return JSON"))},
		Tools: []corechat.ToolDefinition{{
			Name:        "lookup",
			Description: "Look up a record",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		}},
	}
	format, err := corechat.NewOutputFormat(corechat.OutputFormatJSON)
	if err != nil {
		t.Fatal(err)
	}
	request.Options.OutputFormat = &format
	request.Options.ReasoningEffort = "max"
	request.ToolChoice = &corechat.ToolChoice{Mode: corechat.ToolChoiceNamed, Name: "lookup"}
	if err := request.Options.Extensions.Set(deepseek.RequestExtensionKey, deepseek.RequestOptions{
		Thinking:    &deepseek.ThinkingConfig{Type: deepseek.ThinkingEnabled},
		LogProbs:    &logProbs,
		TopLogProbs: &topLogProbs,
		UserID:      "tenant_42-user",
	}); err != nil {
		t.Fatalf("SetExtension: %v", err)
	}
	if _, err := model.Call(t.Context(), request); err != nil {
		t.Fatalf("Call: %v", err)
	}

	thinking, ok := body["thinking"].(map[string]any)
	if !ok || thinking["type"] != "enabled" {
		t.Fatalf("thinking = %#v", body["thinking"])
	}
	if body["reasoning_effort"] != "max" || body["user_id"] != "tenant_42-user" {
		t.Fatalf("DeepSeek fields missing: %#v", body)
	}
	wireFormat, ok := body["response_format"].(map[string]any)
	if !ok || wireFormat["type"] != "json_object" {
		t.Fatalf("response_format = %#v", body["response_format"])
	}
	choice, ok := body["tool_choice"].(map[string]any)
	if !ok || choice["type"] != "function" {
		t.Fatalf("tool_choice = %#v", body["tool_choice"])
	}
	function, ok := choice["function"].(map[string]any)
	if !ok || function["name"] != "lookup" {
		t.Fatalf("tool_choice.function = %#v", choice["function"])
	}
	if body["logprobs"] != true || body["top_logprobs"] != float64(5) {
		t.Fatalf("log probability fields missing: %#v", body)
	}
}

func TestChatThinkingDisabledAllowsSampling(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"chat-1","model":"deepseek-v4-flash","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(server.Close)

	model, err := deepseek.NewChat(t.Context(), deepseek.ChatConfig{
		APIKey:         "test-key",
		BaseURL:        server.URL,
		DefaultOptions: corechat.Options{Model: deepseek.ModelV4Flash},
	})
	if err != nil {
		t.Fatalf("NewChat: %v", err)
	}
	temperature := 0.7
	request := &corechat.Request{
		Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hello"))},
		Options:  corechat.Options{Temperature: &temperature},
	}
	if err := request.Options.Extensions.Set(deepseek.RequestExtensionKey, deepseek.RequestOptions{
		Thinking: &deepseek.ThinkingConfig{Type: deepseek.ThinkingDisabled},
	}); err != nil {
		t.Fatalf("SetExtension: %v", err)
	}
	if _, err := model.Call(t.Context(), request); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if body["temperature"] != temperature {
		t.Fatalf("temperature = %#v; want %v", body["temperature"], temperature)
	}
}

func TestChatMapsStreamingUsageOption(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"id\":\"chat-1\",\"model\":\"deepseek-v4-flash\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	t.Cleanup(server.Close)

	model, err := deepseek.NewChat(t.Context(), deepseek.ChatConfig{
		APIKey:         "test-key",
		BaseURL:        server.URL,
		DefaultOptions: corechat.Options{Model: deepseek.ModelV4Flash},
	})
	if err != nil {
		t.Fatalf("NewChat: %v", err)
	}
	includeUsage := true
	request := &corechat.Request{Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hello"))}}
	if err := request.Options.Extensions.Set(deepseek.RequestExtensionKey, deepseek.RequestOptions{IncludeUsage: &includeUsage}); err != nil {
		t.Fatalf("SetExtension: %v", err)
	}
	for _, streamErr := range model.Stream(t.Context(), request) {
		if streamErr != nil {
			t.Fatalf("Stream: %v", streamErr)
		}
	}
	streamOptions, ok := body["stream_options"].(map[string]any)
	if !ok || streamOptions["include_usage"] != true {
		t.Fatalf("stream_options = %#v", body["stream_options"])
	}
}

func TestChatRejectsInvalidDeepSeekOptions(t *testing.T) {
	model, err := deepseek.NewChat(t.Context(), deepseek.ChatConfig{
		APIKey:         "test-key",
		DefaultOptions: corechat.Options{Model: deepseek.ModelV4Flash},
	})
	if err != nil {
		t.Fatalf("NewChat: %v", err)
	}
	trueValue := true
	topLogProbs := int64(1)
	tests := []struct {
		name    string
		options deepseek.RequestOptions
		core    corechat.Options
		choice  *corechat.ToolChoice
		tools   []corechat.ToolDefinition
		want    string
	}{
		{name: "unknown thinking mode", options: deepseek.RequestOptions{Thinking: &deepseek.ThinkingConfig{Type: "sometimes"}}, want: "thinking.type has unsupported value"},
		{name: "effort without thinking", options: deepseek.RequestOptions{Thinking: &deepseek.ThinkingConfig{Type: deepseek.ThinkingDisabled}}, core: corechat.Options{ReasoningEffort: "high"}, want: "reasoning_effort requires thinking.type=enabled"},
		{name: "unknown effort", core: corechat.Options{ReasoningEffort: "turbo"}, want: "reasoning_effort has unsupported value"},
		{name: "ignored temperature", core: corechat.Options{Temperature: new(0.5)}, want: "temperature has no effect"},
		{name: "top logprobs without logprobs", options: deepseek.RequestOptions{TopLogProbs: &topLogProbs}, want: "top_logprobs requires logprobs=true"},
		{name: "usage on non-streaming call", options: deepseek.RequestOptions{IncludeUsage: &trueValue}, want: "include_usage is valid only for streaming"},
		{name: "invalid user id", options: deepseek.RequestOptions{UserID: "private@example.com"}, want: "user_id may contain only"},
		{
			name:   "missing named tool",
			choice: &corechat.ToolChoice{Mode: corechat.ToolChoiceNamed, Name: "missing"},
			tools:  []corechat.ToolDefinition{{Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)}},
			want:   "undefined tool",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := &corechat.Request{
				Messages:   []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hello"))},
				Options:    test.core,
				ToolChoice: test.choice,
				Tools:      test.tools,
			}
			if err := request.Options.Extensions.Set(deepseek.RequestExtensionKey, test.options); err != nil {
				t.Fatalf("SetExtension: %v", err)
			}
			if _, err := model.Call(t.Context(), request); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Call error = %v; want substring %q", err, test.want)
			}
		})
	}

	request := &corechat.Request{
		Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hello"))},
	}
	if err := request.Options.Extensions.Set(deepseek.RequestExtensionKey, map[string]any{"reasoning_effort": "high"}); err != nil {
		t.Fatal(err)
	}
	if _, err := model.Call(t.Context(), request); err == nil || !strings.Contains(err.Error(), "owned by options.reasoning_effort") {
		t.Fatalf("duplicate reasoning effort owner error = %v", err)
	}

	request.Options = corechat.Options{}
	if err := request.Options.Extensions.Set(deepseek.RequestExtensionKey, map[string]any{"tool_choice": "auto"}); err != nil {
		t.Fatal(err)
	}
	if _, err := model.Call(t.Context(), request); err == nil || !strings.Contains(err.Error(), "owned by options.tool_choice") {
		t.Fatalf("duplicate tool choice owner error = %v", err)
	}
}

func TestNewChatRejectsIgnoredDefaultSampling(t *testing.T) {
	_, err := deepseek.NewChat(t.Context(), deepseek.ChatConfig{
		APIKey:         "test-key",
		DefaultOptions: corechat.Options{Model: deepseek.ModelV4Flash, Temperature: new(0.5)},
	})
	if err == nil || !strings.Contains(err.Error(), "temperature has no effect") {
		t.Fatalf("NewChat error = %v; want ignored temperature error", err)
	}
}

func findAssistantMessage(t *testing.T, messages []map[string]any) map[string]any {
	t.Helper()
	for _, message := range messages {
		if message["role"] == "assistant" {
			return message
		}
	}
	t.Fatal("assistant message not found")
	return nil
}
