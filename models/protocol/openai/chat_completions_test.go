package openai_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/modeltest"
	scopeopenai "github.com/Tangerg/scope/models/protocol/openai"
)

func TestCompatibleChat_CoreConformance(t *testing.T) {
	modeltest.ChatSuite{
		New:              newCoreChatModel,
		Request:          newCoreChatRequest,
		AssertCall:       assertCoreChatCall,
		AssertStream:     assertCoreChatStream,
		AssertAggregated: assertCoreChatAggregated,
	}.Run(t)
}

func newCoreChatModel(t *testing.T) (corechat.Model, corechat.Streamer) {
	t.Helper()
	server := newCoreChatServer(t)
	t.Cleanup(server.Close)
	adapter, err := scopeopenai.NewCompatibleChatCompletions(t.Context(),
		scopeopenai.ChatCompletionsConfig{
			APIKey:         "test-key",
			DefaultOptions: corechat.Options{Model: "gpt-default-must-be-overridden"},
			BaseURL:        server.URL,
		},
		scopeopenai.ReasoningContentDialect("test"),
	)
	if err != nil {
		t.Fatalf("NewChatCompletions: %v", err)
	}
	return adapter, adapter
}

func assertCoreChatCall(t *testing.T, response *corechat.Response) {
	t.Helper()
	if _, found := response.Metadata.Extra["test/openai_response"]; !found {
		t.Fatal("compatible response did not preserve the provider-scoped official response")
	}
	if _, found := response.Metadata.Extra[scopeopenai.ResponseExtensionKey]; found {
		t.Fatal("compatible response leaked into OpenAI's native extension namespace")
	}
	if response.Metadata.ID != "chatcmpl-core" || response.Metadata.Model != "gpt-5.2" {
		t.Fatalf("identity = %q/%q", response.Metadata.ID, response.Metadata.Model)
	}
	result := response.Output
	if result.FinishReason != corechat.FinishReasonToolCalls {
		t.Errorf("finish reason = %q", result.FinishReason)
	}
	if result.Message == nil || len(result.Message.Parts) != 4 {
		t.Fatalf("result message = %#v; want reasoning/text/tool/media", result.Message)
	}
	if result.Message.Parts[0].Kind != corechat.PartReasoning || result.Message.Parts[0].Text != "checking sources" {
		t.Errorf("reasoning part = %#v", result.Message.Parts[0])
	}
	call := result.Message.Parts[2].ToolCall
	if call == nil || call.ID != "call-2" || call.Name != "search" {
		t.Errorf("tool call = %#v", call)
	}
	audio := result.Message.Parts[3].Media
	if audio == nil || audio.MIME != "audio/wav" || audio.Source.Kind != media.SourceReference || audio.Source.Ref != "audio-1" {
		t.Errorf("audio = %#v", audio)
	}
	usage := response.Metadata.Usage
	if usage.InputTokens != 12 || usage.OutputTokens != 7 ||
		usage.ReasoningTokens == nil || *usage.ReasoningTokens != 3 ||
		usage.CacheReadInputTokens == nil || *usage.CacheReadInputTokens != 5 {
		t.Errorf("usage = %#v", usage)
	}
}

func assertCoreChatStream(t *testing.T, responses []*corechat.ResponseDelta) {
	t.Helper()
	var text, reasoning strings.Builder
	var toolIDs []string
	var finalUsage *corechat.Usage
	for index, response := range responses {
		if _, found := response.Metadata.Extra["test/openai_stream_chunk"]; !found {
			t.Error("compatible stream did not preserve a provider-scoped official chunk")
		}
		if _, found := response.Metadata.Extra[scopeopenai.StreamChunkExtensionKey]; found {
			t.Error("compatible stream leaked into OpenAI's native extension namespace")
		}
		if index == len(responses)-1 {
			if response.FinishReason != corechat.FinishReasonToolCalls {
				t.Errorf("terminal finish reason = %q, want %q", response.FinishReason, corechat.FinishReasonToolCalls)
			}
		} else if response.FinishReason != "" {
			t.Errorf("response[%d] has premature finish reason %q", index, response.FinishReason)
		}
		finalUsage = response.Metadata.Usage
		for _, part := range response.Parts {
			switch part.Kind {
			case corechat.PartDeltaText:
				text.WriteString(part.Text)
			case corechat.PartDeltaReasoning:
				reasoning.WriteString(part.Text)
			case corechat.PartDeltaToolCall:
				toolIDs = append(toolIDs, part.ToolCall.ID)
			}
		}
	}
	if text.String() != "hello world" || reasoning.String() != "think " {
		t.Errorf("stream text/reasoning = %q/%q", text.String(), reasoning.String())
	}
	if len(toolIDs) != 2 {
		t.Fatalf("tool deltas = %v", toolIDs)
	}
	for _, id := range toolIDs {
		if id != "call-stream" {
			t.Errorf("unstable tool ID %q", id)
		}
	}
	if finalUsage.InputTokens != 8 || finalUsage.OutputTokens != 4 {
		t.Errorf("final usage = %#v", finalUsage)
	}
}

func assertCoreChatAggregated(t *testing.T, response *corechat.Response) {
	t.Helper()
	if response.Metadata.ID != "chatcmpl-stream" || response.Metadata.Model != "gpt-5.2" || response.Output == nil {
		t.Fatalf("aggregated response = %#v", response)
	}
	result := response.Output
	if result.Message == nil || len(result.Message.Parts) != 3 || result.FinishReason != corechat.FinishReasonToolCalls {
		t.Fatalf("aggregated result = %#v", result)
	}
	call := result.Message.Parts[2].ToolCall
	if result.Message.Parts[0].Text != "think " || result.Message.Parts[1].Text != "hello world" || call == nil || call.Arguments != `{"q":"scope"}` {
		t.Errorf("aggregated parts = %#v; call = %#v", result.Message.Parts, call)
	}
	if response.Metadata.Usage.InputTokens != 8 || response.Metadata.Usage.OutputTokens != 4 {
		t.Errorf("aggregated usage = %#v", response.Metadata.Usage)
	}
}

func TestCompatibleChatRejectsMultipleProviderChoices(t *testing.T) {
	server := modeltest.JSONServer(http.StatusOK, `{
		"id":"chatcmpl-multiple","model":"gpt-5.2","choices":[
			{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"first"}},
			{"index":1,"finish_reason":"stop","message":{"role":"assistant","content":"second"}}
		]
	}`)
	t.Cleanup(server.Close)
	model, err := scopeopenai.NewCompatibleChatCompletions(t.Context(), scopeopenai.ChatCompletionsConfig{
		APIKey: "test-key", BaseURL: server.URL, DefaultOptions: corechat.Options{Model: "gpt-5.2"},
	}, scopeopenai.Dialect{Provider: "test", TokenLimitField: scopeopenai.TokenLimitMaxTokens})
	if err != nil {
		t.Fatalf("NewCompatibleChatCompletions: %v", err)
	}
	request, err := corechat.NewRequest(corechat.NewUserMessage(corechat.NewTextPart("hello")))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if _, err := model.Call(t.Context(), request); err == nil || !strings.Contains(err.Error(), "supports one output") {
		t.Fatalf("Call error = %v; want multiple-choice rejection", err)
	}
}

func TestCompatibleChatRejectsResultCountOption(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("request must fail before provider I/O")
	}))
	t.Cleanup(server.Close)
	model, err := scopeopenai.NewCompatibleChatCompletions(t.Context(), scopeopenai.ChatCompletionsConfig{
		APIKey: "test-key", BaseURL: server.URL, DefaultOptions: corechat.Options{Model: "gpt-5.2"},
	}, scopeopenai.Dialect{Provider: "test", TokenLimitField: scopeopenai.TokenLimitMaxTokens})
	if err != nil {
		t.Fatalf("NewCompatibleChatCompletions: %v", err)
	}
	request, err := corechat.NewRequest(corechat.NewUserMessage(corechat.NewTextPart("hello")))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if err := request.Options.Extensions.Set("test/openai_request", map[string]any{"n": 2}); err != nil {
		t.Fatalf("SetExtension: %v", err)
	}
	if _, err := model.Call(t.Context(), request); err == nil || !strings.Contains(err.Error(), "produces one output") {
		t.Fatalf("Call error = %v; want output-count rejection", err)
	}
}

func newCoreChatRequest(t *testing.T) *corechat.Request {
	t.Helper()
	image, err := media.NewBytes("image/png", []byte("image"))
	if err != nil {
		t.Fatalf("NewBytes: %v", err)
	}
	image.Name = "diagram.png"
	file, err := media.NewReference("application/pdf", "file-123")
	if err != nil {
		t.Fatalf("NewReference: %v", err)
	}
	file.Name = "spec.pdf"

	previousAudio, err := media.NewReference("audio/wav", "audio-prev")
	if err != nil {
		t.Fatalf("NewReference: %v", err)
	}
	assistant := corechat.NewAssistantMessage(
		corechat.NewTextPart("I will search."),
		corechat.NewToolCallPart(corechat.ToolCall{ID: "call-1", Name: "search", Arguments: `{"q":"scope"}`}),
		corechat.NewMediaPart(previousAudio),
	)

	request, err := corechat.NewRequest(
		corechat.NewSystemMessage("You are precise."),
		corechat.NewUserMessage(corechat.NewTextPart("Inspect these inputs."), corechat.NewMediaPart(image), corechat.NewMediaPart(file)),
		assistant,
		corechat.NewToolMessage(corechat.ToolResult{
			ID: "call-1", Name: "search", Output: corechat.NewTextToolOutput(`{"hits":2}`),
		}),
	)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	temperature := 0.3
	maxTokens := int64(512)
	format, err := corechat.NewOutputFormat(corechat.OutputFormatJSON)
	if err != nil {
		t.Fatalf("NewOutputFormat: %v", err)
	}
	request.Options = corechat.Options{Model: "gpt-5.2", OutputFormat: &format, Temperature: &temperature, MaxOutputTokens: &maxTokens, Stop: []string{"<END>"}}
	request.Tools = []corechat.ToolDefinition{{
		Name:        "search",
		Description: "Search the index",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}`),
	}}
	request.ToolChoice = &corechat.ToolChoice{
		Mode: corechat.ToolChoiceNamed, Name: "search", Parallelism: corechat.ToolParallelismSingle,
	}
	if err := request.Options.Extensions.Set("test/openai_request", map[string]any{
		"modalities": []string{"text", "audio"},
		"audio":      map[string]any{"format": "wav", "voice": "alloy"},
	}); err != nil {
		t.Fatalf("SetExtension: %v", err)
	}
	return request
}

func newCoreChatServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			Model             string            `json:"model"`
			Stream            bool              `json:"stream"`
			Messages          []json.RawMessage `json:"messages"`
			Tools             []json.RawMessage `json:"tools"`
			ToolChoice        json.RawMessage   `json:"tool_choice"`
			ParallelToolCalls *bool             `json:"parallel_tool_calls"`
			Modalities        []string          `json:"modalities"`
			MaxTokens         int64             `json:"max_tokens"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(writer, "bad request", http.StatusBadRequest)
			return
		}
		if request.URL.Path != "/chat/completions" || request.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("request identity = %q/%q", request.URL.Path, request.Header.Get("Authorization"))
		}
		if body.Model != "gpt-5.2" || len(body.Messages) != 4 || len(body.Tools) != 1 || body.MaxTokens != 512 {
			t.Errorf("request shape = model %q messages %d tools %d max %d", body.Model, len(body.Messages), len(body.Tools), body.MaxTokens)
		}
		if body.ParallelToolCalls == nil || *body.ParallelToolCalls || !strings.Contains(string(body.ToolChoice), `"name":"search"`) {
			t.Errorf("tool choice = %s / %#v", body.ToolChoice, body.ParallelToolCalls)
		}
		if strings.Join(body.Modalities, ",") != "text,audio" {
			t.Errorf("modalities = %v", body.Modalities)
		}
		var assistant struct {
			Audio struct {
				ID string `json:"id"`
			} `json:"audio"`
		}
		if err := json.Unmarshal(body.Messages[2], &assistant); err != nil || assistant.Audio.ID != "audio-prev" {
			t.Errorf("assistant audio replay = %q/%v", assistant.Audio.ID, err)
		}
		if body.Stream {
			writeCoreChatStream(writer)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, coreChatCompletionJSON)
	}))
}

func writeCoreChatStream(writer http.ResponseWriter) {
	writer.Header().Set("Content-Type", "text/event-stream")
	chunks := []string{
		`{"id":"chatcmpl-stream","object":"chat.completion.chunk","created":1770000001,"model":"gpt-5.2","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		`{"id":"chatcmpl-stream","object":"chat.completion.chunk","created":1770000001,"model":"gpt-5.2","choices":[{"index":0,"delta":{"reasoning_content":"think "}}]}`,
		`{"id":"chatcmpl-stream","object":"chat.completion.chunk","created":1770000001,"model":"gpt-5.2","choices":[{"index":0,"delta":{"content":"hello "}}]}`,
		`{"id":"chatcmpl-stream","object":"chat.completion.chunk","created":1770000001,"model":"gpt-5.2","choices":[{"index":0,"delta":{"content":"world"}}]}`,
		`{"id":"chatcmpl-stream","object":"chat.completion.chunk","created":1770000001,"model":"gpt-5.2","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"q\":"}}]}}]}`,
		`{"id":"chatcmpl-stream","object":"chat.completion.chunk","created":1770000001,"model":"gpt-5.2","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-stream","type":"function","function":{"name":"search","arguments":"\"scope\""}}]}}]}`,
		`{"id":"chatcmpl-stream","object":"chat.completion.chunk","created":1770000001,"model":"gpt-5.2","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"}"}}]},"finish_reason":"tool_calls"}]}`,
		`{"id":"chatcmpl-stream","object":"chat.completion.chunk","created":1770000001,"model":"gpt-5.2","choices":[],"usage":{"prompt_tokens":8,"completion_tokens":4,"total_tokens":12}}`,
	}
	for _, chunk := range chunks {
		fmt.Fprintf(writer, "data: %s\n\n", chunk)
	}
	fmt.Fprint(writer, "data: [DONE]\n\n")
}

var coreChatCompletionJSON = `{
  "id":"chatcmpl-core",
  "object":"chat.completion",
  "created":1770000000,
  "model":"gpt-5.2",
  "service_tier":"priority",
  "choices":[
    {
      "index":0,
      "finish_reason":"tool_calls",
      "message":{
        "role":"assistant",
        "reasoning_content":"checking sources",
        "content":"I found two results.",
        "refusal":"",
        "annotations":[],
        "tool_calls":[{"id":"call-2","type":"function","function":{"name":"search","arguments":"{\"q\":\"more\"}"}}],
        "audio":{"id":"audio-1","data":"` + base64.StdEncoding.EncodeToString([]byte("audio")) + `","expires_at":1770000100,"transcript":"spoken"}
      },
      "logprobs":{"content":[],"refusal":[]}
    }
  ],
  "usage":{
    "prompt_tokens":12,
    "completion_tokens":7,
    "total_tokens":19,
    "completion_tokens_details":{"reasoning_tokens":3},
    "prompt_tokens_details":{"cached_tokens":5}
  }
}`
