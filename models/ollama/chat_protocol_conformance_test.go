package ollama_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/ollama"
)

func TestChat_CoreConformance(t *testing.T) {
	modeltest.ChatSuite{
		New: func(t *testing.T) (corechat.Model, corechat.Streamer) {
			t.Helper()
			server := newProtocolChatServer(t)
			t.Cleanup(server.Close)
			adapter, err := ollama.NewChat(t.Context(), ollama.ChatConfig{
				DefaultOptions: corechat.Options{Model: "default-must-be-overridden"},
				BaseURL:        server.URL,
			})
			if err != nil {
				t.Fatalf("NewChat: %v", err)
			}
			return adapter, adapter
		},
		Request: newProtocolChatRequest,
		AssertCall: func(t *testing.T, response *corechat.Response) {
			t.Helper()
			assertProtocolResponse(t, response)
		},
		AssertStream: func(t *testing.T, responses []*corechat.ResponseDelta) {
			t.Helper()
			var reasoning, content strings.Builder
			var toolCall *corechat.ToolCall
			mediaParts := 0
			var final *corechat.ResponseDelta
			for _, response := range responses {
				final = response
				for _, part := range response.Parts {
					switch part.Kind {
					case corechat.PartDeltaReasoning:
						reasoning.WriteString(part.Text)
					case corechat.PartDeltaText:
						content.WriteString(part.Text)
					case corechat.PartDeltaMedia:
						mediaParts++
					case corechat.PartDeltaToolCall:
						toolCall = &corechat.ToolCall{ID: part.ToolCall.ID, Name: part.ToolCall.Name, Arguments: part.ToolCall.Arguments}
					}
				}
			}
			if reasoning.String() != "inspect colors" || content.String() != "It is a blue square." || mediaParts != 1 {
				t.Errorf("stream reasoning/text/media = %q/%q/%d", reasoning.String(), content.String(), mediaParts)
			}
			if toolCall == nil || toolCall.ID != "ollama/generated/0" || toolCall.Name != "inspect" || toolCall.Arguments != `{"detail":true}` {
				t.Errorf("stream tool call = %#v", toolCall)
			}
			if final == nil || final.Metadata == nil || final.Metadata.Usage.InputTokens != 11 || final.Metadata.Usage.OutputTokens != 5 {
				t.Errorf("final usage = %#v", final)
			}
		},
		AssertAggregated: func(t *testing.T, response *corechat.Response) {
			t.Helper()
			assertProtocolResponse(t, response)
		},
	}.Run(t)
}

func TestOpenAIChatConstructor(t *testing.T) {
	model, err := ollama.NewChatCompletions(t.Context(), ollama.ChatCompletionsConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if model == nil {
		t.Fatal("NewChatCompletions(t.Context(), ) = nil")
	}
}

func TestChat_RejectsUnsupportedInputBeforeProviderIO(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hits.Add(1)
		http.Error(writer, "unexpected provider call", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	adapter, err := ollama.NewChat(t.Context(), ollama.ChatConfig{
		DefaultOptions: corechat.Options{Model: "qwen3:8b"},
		BaseURL:        server.URL,
	})
	if err != nil {
		t.Fatalf("NewChat: %v", err)
	}

	uriImage, err := media.NewURI("image/png", "https://example.com/image.png")
	if err != nil {
		t.Fatalf("NewURI: %v", err)
	}
	tests := []struct {
		name    string
		message corechat.Message
		want    string
	}{
		{
			name:    "reasoning signature",
			message: corechat.NewAssistantMessage(corechat.NewReasoningPart("thinking", []byte("opaque"))),
			want:    "reasoning signature is unsupported",
		},
		{
			name:    "URI image",
			message: corechat.NewUserMessage(corechat.NewMediaPart(uriImage)),
			want:    "Ollama requires bytes",
		},
		{
			name: "non-object tool arguments",
			message: corechat.NewAssistantMessage(corechat.NewToolCallPart(corechat.ToolCall{
				ID: "call-1", Name: "inspect", Arguments: `[true]`,
			})),
			want: "cannot unmarshal array",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, err := corechat.NewRequest(test.message)
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			_, err = adapter.Call(t.Context(), request)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Call error = %v; want substring %q", err, test.want)
			}
		})
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("provider HTTP calls = %d; want 0", got)
	}
}

func TestChatRejectsToolChoiceAndDuplicateExtensionOptions(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(writer, "unexpected provider call", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	adapter, err := ollama.NewChat(t.Context(), ollama.ChatConfig{
		DefaultOptions: corechat.Options{Model: "qwen3:8b"}, BaseURL: server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := &corechat.Request{
		Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hello"))},
		Tools: []corechat.ToolDefinition{{
			Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`),
		}},
		ToolChoice: &corechat.ToolChoice{Mode: corechat.ToolChoiceAuto},
	}
	if _, err := adapter.Call(t.Context(), request); err == nil || !strings.Contains(err.Error(), "tool choice is not supported") {
		t.Fatalf("tool choice error = %v", err)
	}
	request.ToolChoice = nil
	if err := request.Options.Extensions.Set(ollama.RequestExtensionKey, map[string]any{
		"options": map[string]any{"temperature": 0.5},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Call(t.Context(), request); err == nil || !strings.Contains(err.Error(), "owned by Core chat options") {
		t.Fatalf("duplicate extension error = %v", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("provider HTTP calls = %d", hits.Load())
	}
}

func newProtocolChatRequest(t *testing.T) *corechat.Request {
	t.Helper()
	image, err := media.NewBytes("image/png", []byte("image"))
	if err != nil {
		t.Fatalf("NewBytes: %v", err)
	}
	request, err := corechat.NewRequest(
		corechat.NewSystemMessage("Be concise."),
		corechat.NewUserMessage(corechat.NewTextPart("Describe this image."), corechat.NewMediaPart(image)),
		corechat.NewAssistantMessage(
			corechat.NewReasoningPart("inspect pixels", nil),
			corechat.NewTextPart("I will inspect it."),
			corechat.NewToolCallPart(corechat.ToolCall{ID: "ollama/0/2", Name: "inspect", Arguments: `{"detail":true}`}),
		),
		corechat.NewToolMessage(corechat.ToolResult{
			ID: "ollama/0/2", Name: "inspect", Output: corechat.NewTextToolOutput("blue square"),
		}),
	)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	frequencyPenalty := 0.1
	maxTokens := int64(256)
	presencePenalty := 0.1
	temperature := 0.5
	topK := int64(20)
	topP := 0.8
	request.Options = corechat.Options{
		Model:            "qwen3:8b",
		FrequencyPenalty: &frequencyPenalty,
		MaxOutputTokens:  &maxTokens,
		PresencePenalty:  &presencePenalty,
		Stop:             []string{"END"},
		Temperature:      &temperature,
		TopK:             &topK,
		TopP:             &topP,
	}
	request.Tools = []corechat.ToolDefinition{{
		Name:        "inspect",
		Description: "Inspect image details",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"detail":{"type":"boolean"}}}`),
	}}
	format, err := corechat.NewOutputFormat(corechat.OutputFormatJSON)
	if err != nil {
		t.Fatalf("NewOutputFormat: %v", err)
	}
	request.Options.OutputFormat = &format
	if err := request.Options.Extensions.Set(ollama.RequestExtensionKey, map[string]any{
		"keep_alive": "10m",
		"think":      true,
		"options": map[string]any{
			"seed":    42,
			"num_ctx": 8192,
		},
	}); err != nil {
		t.Fatalf("SetExtension: %v", err)
	}
	return request
}

func newProtocolChatServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/chat" {
			t.Errorf("request path = %q", request.URL.Path)
		}
		var body protocolChatRequestWire
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(writer, "bad request", http.StatusBadRequest)
			return
		}
		assertProtocolRequestWire(t, body)
		writer.Header().Set("Content-Type", "application/x-ndjson")
		if body.Stream != nil && *body.Stream {
			writeProtocolChatStream(writer)
			return
		}
		fmt.Fprintln(writer, protocolChatResponseJSON)
	}))
}

type protocolChatRequestWire struct {
	Model     string                `json:"model"`
	Messages  []protocolMessageWire `json:"messages"`
	Stream    *bool                 `json:"stream"`
	Format    string                `json:"format"`
	KeepAlive string                `json:"keep_alive"`
	Think     bool                  `json:"think"`
	Options   map[string]any        `json:"options"`
	Tools     []struct {
		Type     string `json:"type"`
		Function struct {
			Name       string `json:"name"`
			Parameters struct {
				Type       string         `json:"type"`
				Properties map[string]any `json:"properties"`
			} `json:"parameters"`
		} `json:"function"`
	} `json:"tools"`
}

type protocolMessageWire struct {
	Role       string   `json:"role"`
	Content    string   `json:"content"`
	Thinking   string   `json:"thinking"`
	Images     []string `json:"images"`
	ToolName   string   `json:"tool_name"`
	ToolCallID string   `json:"tool_call_id"`
	ToolCalls  []struct {
		ID       string `json:"id"`
		Function struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

func assertProtocolRequestWire(t *testing.T, body protocolChatRequestWire) {
	t.Helper()
	assertProtocolRequestIdentity(t, body)
	assertProtocolMessages(t, body.Messages)
	assertProtocolTools(t, body)
	assertProtocolOptions(t, body.Options)
}

func assertProtocolRequestIdentity(t *testing.T, body protocolChatRequestWire) {
	t.Helper()
	if body.Model != "qwen3:8b" || body.Stream == nil || body.Format != "json" || body.KeepAlive != "10m0s" || !body.Think {
		t.Errorf("request identity/native config = %#v", body)
	}
}

func assertProtocolMessages(t *testing.T, messages []protocolMessageWire) {
	t.Helper()
	if len(messages) != 4 || messages[0].Role != "system" || messages[1].Role != "user" || messages[2].Role != "assistant" || messages[3].Role != "tool" {
		t.Fatalf("messages = %#v", messages)
	}
	if messages[1].Content != "Describe this image." || len(messages[1].Images) != 1 || messages[1].Images[0] != "aW1hZ2U=" {
		t.Errorf("user message = %#v", messages[1])
	}
	assistant := messages[2]
	if assistant.Thinking != "inspect pixels" || assistant.Content != "I will inspect it." || len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].ID != "ollama/0/2" || assistant.ToolCalls[0].Function.Arguments["detail"] != true {
		t.Errorf("assistant message = %#v", assistant)
	}
	tool := messages[3]
	if tool.Content != "blue square" || tool.ToolName != "inspect" || tool.ToolCallID != "ollama/0/2" {
		t.Errorf("tool message = %#v", tool)
	}
}

func assertProtocolTools(t *testing.T, body protocolChatRequestWire) {
	t.Helper()
	if len(body.Tools) != 1 || body.Tools[0].Type != "function" || body.Tools[0].Function.Name != "inspect" || body.Tools[0].Function.Parameters.Type != "object" || len(body.Tools[0].Function.Parameters.Properties) != 1 {
		t.Errorf("tools = %#v", body.Tools)
	}
}

func assertProtocolOptions(t *testing.T, options map[string]any) {
	t.Helper()
	if options["seed"] != float64(42) || options["num_ctx"] != float64(8192) || options["temperature"] != float64(0.5) ||
		options["num_predict"] != float64(256) || options["top_k"] != float64(20) || options["top_p"] != float64(0.8) ||
		options["frequency_penalty"] != float64(0.1) || options["presence_penalty"] != float64(0.1) {
		t.Errorf("options = %#v", options)
	}
}

func assertProtocolResponse(t *testing.T, response *corechat.Response) {
	t.Helper()
	if response.Metadata.Model != "qwen3:8b" || response.Output == nil {
		t.Fatalf("response = %#v", response)
	}
	result := response.Output
	if result.Message == nil || len(result.Message.Parts) != 4 || result.FinishReason != corechat.FinishReasonToolCalls {
		t.Fatalf("result = %#v", result)
	}
	if result.Message.Parts[0].Kind != corechat.PartReasoning || result.Message.Parts[0].Text != "inspect colors" ||
		result.Message.Parts[1].Kind != corechat.PartText || result.Message.Parts[1].Text != "It is a blue square." {
		t.Errorf("reasoning/text = %#v", result.Message.Parts)
	}
	mediaPart := result.Message.Parts[2]
	if mediaPart.Kind != corechat.PartMedia || mediaPart.Media == nil {
		t.Errorf("media = %#v", mediaPart)
	} else if data, err := mediaPart.Media.Bytes(); err != nil || string(data) != "image" {
		t.Errorf("media bytes = %q, %v", data, err)
	}
	call := result.Message.Parts[3].ToolCall
	if call == nil || call.ID != "ollama/generated/0" || call.Name != "inspect" || call.Arguments != `{"detail":true}` {
		t.Errorf("tool call = %#v", call)
	}
	if response.Metadata.Usage.InputTokens != 11 || response.Metadata.Usage.OutputTokens != 5 {
		t.Errorf("usage = %#v", response.Metadata.Usage)
	}
	if want := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC); !response.Metadata.CreatedAt.Equal(want) {
		t.Errorf("CreatedAt = %s, want %s", response.Metadata.CreatedAt, want)
	}
	durations := decodeExtension[map[string]int64](t, response.Metadata.Extra, "ollama/durations_ns")
	if durations["total"] != 1_250_000_000 || durations["load"] != 100_000_000 || durations["prompt_eval"] != 300_000_000 || durations["eval"] != 700_000_000 {
		t.Errorf("durations = %#v", durations)
	}
	metrics := decodeExtension[map[string]int](t, response.Metadata.Extra, "ollama/metrics")
	if metrics["prompt_eval_count"] != 11 || metrics["eval_count"] != 5 {
		t.Errorf("metrics = %#v", metrics)
	}
	nativeReason := decodeExtension[string](t, result.Metadata.Extra, "ollama/native_done_reason")
	if nativeReason != "stop" {
		t.Errorf("native done reason = %q", nativeReason)
	}
	nativeResponse := decodeExtension[map[string]any](t, response.Metadata.Extra, ollama.ResponseExtensionKey)
	future, ok := nativeResponse["future_field"].(map[string]any)
	if !ok || future["kept"] != true {
		t.Errorf("native response extension lost unknown provider fields: %#v", nativeResponse)
	}
}

func decodeExtension[T any](t *testing.T, values metadata.Map, key string) T {
	t.Helper()
	value, found, err := values.Decode[T](key)
	if err != nil {
		t.Fatalf("decode extension %q: %v", key, err)
	}
	if !found {
		t.Fatalf("extension %q not found", key)
	}
	return value
}

func writeProtocolChatStream(writer http.ResponseWriter) {
	chunks := []string{
		`{"model":"qwen3:8b","message":{"role":"assistant","thinking":"inspect colors"},"done":false}`,
		`{"model":"qwen3:8b","message":{"role":"assistant","content":"It is a blue square."},"done":false}`,
		`{"model":"qwen3:8b","message":{"role":"assistant","images":["aW1hZ2U="]},"done":false}`,
		`{"model":"qwen3:8b","message":{"role":"assistant","tool_calls":[{"function":{"name":"inspect","arguments":{"detail":true}}}]},"done":false}`,
		`{"model":"qwen3:8b","created_at":"2026-07-14T12:00:00Z","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","total_duration":1250000000,"load_duration":100000000,"prompt_eval_count":11,"prompt_eval_duration":300000000,"eval_count":5,"eval_duration":700000000,"future_field":{"kept":true}}`,
	}
	for _, chunk := range chunks {
		fmt.Fprintln(writer, chunk)
	}
}

const protocolChatResponseJSON = `{"model":"qwen3:8b","created_at":"2026-07-14T12:00:00Z","message":{"role":"assistant","thinking":"inspect colors","content":"It is a blue square.","images":["aW1hZ2U="],"tool_calls":[{"function":{"name":"inspect","arguments":{"detail":true}}}]},"done":true,"done_reason":"stop","total_duration":1250000000,"load_duration":100000000,"prompt_eval_count":11,"prompt_eval_duration":300000000,"eval_count":5,"eval_duration":700000000,"future_field":{"kept":true}}`
