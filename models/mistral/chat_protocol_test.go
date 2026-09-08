package mistral_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/mistral"
)

func TestChatMapsNativeThinkingAndReplaysIt(t *testing.T) {
	requests := make([]map[string]any, 0, 2)
	server := newThinkingReplayServer(t, &requests)
	t.Cleanup(server.Close)

	maxTokens := int64(2048)
	model, err := mistral.NewChat(t.Context(), mistral.ChatConfig{
		APIKey:  "test-key",
		BaseURL: server.URL,
		DefaultOptions: corechat.Options{
			Model:           "mistral-medium-3-5",
			MaxOutputTokens: &maxTokens,
		},
	})
	if err != nil {
		t.Fatalf("NewChat: %v", err)
	}
	firstRequest := &corechat.Request{
		Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("find it"))},
		Tools: []corechat.ToolDefinition{{
			Name: "lookup", Description: "Look up an ID", InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"integer"}},"required":["id"]}`),
		}},
	}
	firstRequest.ToolChoice = &corechat.ToolChoice{
		Mode: corechat.ToolChoiceAuto, Parallelism: corechat.ToolParallelismSingle,
	}
	if setExtensionErr := firstRequest.Options.Extensions.Set(mistral.RequestExtensionKey, mistral.ChatRequestOptions{
		ReasoningEffort: mistral.ReasoningEffortHigh,
	}); setExtensionErr != nil {
		t.Fatalf("SetExtension: %v", setExtensionErr)
	}
	firstResponse, err := model.Call(t.Context(), firstRequest)
	if err != nil {
		t.Fatalf("first Call: %v", err)
	}
	message := assertThinkingResponse(t, requests, maxTokens, firstResponse)

	secondRequest := &corechat.Request{Messages: []corechat.Message{
		firstRequest.Messages[0],
		message.Clone(),
		corechat.NewToolMessage(corechat.ToolResult{
			ID: "call-1", Name: "lookup", Output: corechat.NewTextToolOutput(`{"name":"scope"}`),
		}),
	}}
	if _, err := model.Call(t.Context(), secondRequest); err != nil {
		t.Fatalf("second Call: %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %d; want 2", len(requests))
	}
	messages := requests[1]["messages"].([]any)
	assistant := messages[1].(map[string]any)
	content := assistant["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("assistant content = %#v", content)
	}
	thinking := content[0].(map[string]any)
	if thinking["type"] != "thinking" || thinking["closed"] != true {
		t.Fatalf("thinking replay = %#v", thinking)
	}
	nested := thinking["thinking"].([]any)
	if len(nested) != 1 || nested[0].(map[string]any)["text"] != "inspect inputs" {
		t.Fatalf("nested thinking replay = %#v", nested)
	}
}

func newThinkingReplayServer(t *testing.T, requests *[]map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		*requests = append(*requests, body)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
			"id":"cmpl-1",
			"model":"mistral-medium-3-5",
			"choices":[{
				"index":0,
				"finish_reason":"tool_calls",
				"message":{
					"role":"assistant",
					"content":[
						{"type":"thinking","thinking":[{"type":"text","text":"inspect inputs"}],"closed":true},
						{"type":"text","text":"I need a lookup."}
					],
					"tool_calls":[{"id":"call-1","type":"function","index":0,"function":{"name":"lookup","arguments":{"id":7}}}]
				}
			}],
			"usage":{"prompt_tokens":10,"completion_tokens":6,"total_tokens":16,"prompt_tokens_details":{"cached_tokens":4}}
		}`))
	}))
}

func assertThinkingResponse(t *testing.T, requests []map[string]any, maxTokens int64, response *corechat.Response) *corechat.Message {
	t.Helper()
	if len(requests) != 1 || requests[0]["max_tokens"] != float64(maxTokens) || requests[0]["reasoning_effort"] != "high" ||
		requests[0]["tool_choice"] != "auto" || requests[0]["parallel_tool_calls"] != false {
		t.Fatalf("first wire request = %#v", requests)
	}
	result := response.Output
	if result.FinishReason != corechat.FinishReasonToolCalls || result.Message == nil || len(result.Message.Parts) != 3 {
		t.Fatalf("result = %#v", result)
	}
	if result.Message.Parts[0].Kind != corechat.PartReasoning || result.Message.Parts[0].Text != "inspect inputs" || len(result.Message.Parts[0].ReasoningState) == 0 {
		t.Errorf("reasoning = %#v", result.Message.Parts[0])
	}
	if result.Message.Parts[1].Text != "I need a lookup." {
		t.Errorf("text = %#v", result.Message.Parts[1])
	}
	call := result.Message.Parts[2].ToolCall
	if call == nil || call.ID != "call-1" || call.Name != "lookup" || call.Arguments != `{"id":7}` {
		t.Errorf("tool call = %#v", call)
	}
	usage := response.Metadata.Usage
	if usage.InputTokens != 10 || usage.OutputTokens != 6 || usage.CacheReadInputTokens == nil || *usage.CacheReadInputTokens != 4 {
		t.Errorf("usage = %#v", usage)
	}
	return result.Message
}

func TestChatMapsReferenceChunksToCitations(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{"id":"cmpl-1","model":"mistral-small-latest","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":[{"type":"text","text":"Grounded answer."},{"type":"reference","reference_ids":[7,"doc-9"]}]}}],"usage":{}}`)
	}))
	t.Cleanup(server.Close)
	model, err := mistral.NewChat(t.Context(), mistral.ChatConfig{
		APIKey: "test-key", BaseURL: server.URL, DefaultOptions: corechat.Options{Model: "mistral-small-latest"},
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := model.Call(t.Context(), &corechat.Request{Messages: []corechat.Message{
		corechat.NewUserMessage(corechat.NewTextPart("answer with sources")),
	}})
	if err != nil {
		t.Fatal(err)
	}
	part := response.Output.Message.Parts[0]
	if len(part.Citations) != 2 || part.Citations[0].Source.Value != "7" || part.Citations[1].Source.Value != "doc-9" {
		t.Fatalf("citations = %#v", part.Citations)
	}
}

func TestChatRejectsDuplicateToolChoiceSurfacesBeforeProviderIO(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(writer, "unexpected", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	model, err := mistral.NewChat(t.Context(), mistral.ChatConfig{
		APIKey: "test-key", BaseURL: server.URL, DefaultOptions: corechat.Options{Model: "mistral-small-latest"},
	})
	if err != nil {
		t.Fatal(err)
	}
	definition := corechat.ToolDefinition{Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)}
	request := &corechat.Request{
		Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("look up"))},
		Tools:    []corechat.ToolDefinition{definition},
		ToolChoice: &corechat.ToolChoice{
			Mode: corechat.ToolChoiceNamed, Name: "lookup",
		},
	}
	if _, err := model.Call(t.Context(), request); err == nil || !strings.Contains(err.Error(), "named tool choice") {
		t.Fatalf("named tool choice error = %v", err)
	}
	request.ToolChoice = nil
	if err := request.Options.Extensions.Set(mistral.RequestExtensionKey, map[string]any{"tool_choice": "auto"}); err != nil {
		t.Fatal(err)
	}
	if _, err := model.Call(t.Context(), request); err == nil || !strings.Contains(err.Error(), "owned by the Core ToolChoice") {
		t.Fatalf("extension tool choice error = %v", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("provider calls = %d", hits.Load())
	}
}

func TestChatCoalescesStreamedThinkingForReplay(t *testing.T) {
	var replayRequest map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		if streaming, _ := body["stream"].(bool); streaming {
			writer.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(writer, "data: {\"id\":\"cmpl-stream\",\"model\":\"mistral-small-latest\",\"choices\":[{\"index\":0,\"finish_reason\":null,\"delta\":{\"role\":\"assistant\",\"content\":[{\"type\":\"thinking\",\"thinking\":[{\"type\":\"text\",\"text\":\"plan \"}],\"closed\":false}]}}]}\n\n")
			fmt.Fprint(writer, "data: {\"id\":\"cmpl-stream\",\"model\":\"mistral-small-latest\",\"choices\":[{\"index\":0,\"finish_reason\":null,\"delta\":{\"content\":[{\"type\":\"thinking\",\"thinking\":[{\"type\":\"text\",\"text\":\"next\"}],\"closed\":true},{\"type\":\"text\",\"text\":\"answer \"}]}}]}\n\n")
			fmt.Fprint(writer, "data: {\"id\":\"cmpl-stream\",\"model\":\"mistral-small-latest\",\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"delta\":{\"content\":\"done\"}}]}\n\n")
			fmt.Fprint(writer, "data: [DONE]\n\n")
			return
		}
		replayRequest = body
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"cmpl-2","model":"mistral-small-latest","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(server.Close)

	model, err := mistral.NewChat(t.Context(), mistral.ChatConfig{
		APIKey: "test-key", BaseURL: server.URL, DefaultOptions: corechat.Options{Model: "mistral-small-latest"},
	})
	if err != nil {
		t.Fatalf("NewChat: %v", err)
	}
	user := corechat.NewUserMessage(corechat.NewTextPart("solve"))
	var accumulator corechat.ResponseAccumulator
	for response, streamErr := range model.Stream(t.Context(), &corechat.Request{Messages: []corechat.Message{user}}) {
		if streamErr != nil {
			t.Fatalf("Stream: %v", streamErr)
		}
		if addErr := accumulator.Add(response); addErr != nil {
			t.Fatalf("accumulate: %v", addErr)
		}
	}
	aggregated, err := accumulator.Response()
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if aggregated == nil || aggregated.Output == nil || aggregated.Output.Message == nil {
		t.Fatalf("aggregated = %#v", aggregated)
	}
	message := aggregated.Output.Message
	if len(message.Parts) != 2 || message.Parts[0].Text != "plan next" || message.Parts[1].Text != "answer done" {
		t.Fatalf("aggregated parts = %#v", message.Parts)
	}
	if _, err := model.Call(t.Context(), &corechat.Request{Messages: []corechat.Message{
		user, message.Clone(), corechat.NewUserMessage(corechat.NewTextPart("continue")),
	}}); err != nil {
		t.Fatalf("replay Call: %v", err)
	}
	messages := replayRequest["messages"].([]any)
	thinking := messages[1].(map[string]any)["content"].([]any)[0].(map[string]any)
	if thinking["closed"] != true {
		t.Fatalf("thinking replay = %#v", thinking)
	}
	nested := thinking["thinking"].([]any)
	if len(nested) != 2 || nested[0].(map[string]any)["text"] != "plan " || nested[1].(map[string]any)["text"] != "next" {
		t.Fatalf("thinking fragments = %#v", nested)
	}
}

func TestChatReturnsStructuredAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("request-id", "req-123")
		writer.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = writer.Write([]byte(`{"message":"invalid reasoning_effort"}`))
	}))
	t.Cleanup(server.Close)

	model, err := mistral.NewChat(t.Context(), mistral.ChatConfig{
		APIKey: "test-key", BaseURL: server.URL, DefaultOptions: corechat.Options{Model: "mistral-small-latest"},
	})
	if err != nil {
		t.Fatalf("NewChat: %v", err)
	}
	_, err = model.Call(t.Context(), &corechat.Request{Messages: []corechat.Message{
		corechat.NewUserMessage(corechat.NewTextPart("hello")),
	}})
	var apiError *mistral.APIError
	if !errors.As(err, &apiError) {
		t.Fatalf("error = %T %v; want *mistral.APIError", err, err)
	}
	if apiError.StatusCode != http.StatusUnprocessableEntity || apiError.RequestID != "req-123" || apiError.Message != "invalid reasoning_effort" {
		t.Fatalf("API error = %#v", apiError)
	}
}

func TestChatStreamSurfacesMalformedEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\n\n")
	}))
	t.Cleanup(server.Close)

	model, err := mistral.NewChat(t.Context(), mistral.ChatConfig{
		APIKey: "test-key", BaseURL: server.URL, DefaultOptions: corechat.Options{Model: "mistral-small-latest"},
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := corechat.NewRequest(corechat.NewUserMessage(corechat.NewTextPart("hello")))
	if err != nil {
		t.Fatal(err)
	}
	for _, streamErr := range model.Stream(t.Context(), request) {
		if streamErr == nil || !strings.Contains(streamErr.Error(), "decode chat stream chunk") {
			t.Fatalf("Stream error = %v, want malformed event error", streamErr)
		}
		return
	}
	t.Fatal("Stream completed without surfacing malformed event")
}
