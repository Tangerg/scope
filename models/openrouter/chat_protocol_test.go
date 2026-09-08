package openrouter_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/openrouter"
)

func TestAnthropicChatConstructorValidatesCredential(t *testing.T) {
	if _, err := openrouter.NewMessages(t.Context(), openrouter.MessagesConfig{}); err == nil {
		t.Fatal("NewMessages(t.Context(), ) accepted an absent credential")
	}
	model, err := openrouter.NewMessages(t.Context(), openrouter.MessagesConfig{APIKey: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	if model == nil {
		t.Fatal("NewMessages(t.Context(), ) = nil")
	}
}

func TestChatPreservesStructuredReasoningDetails(t *testing.T) {
	requests := make([]map[string]any, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		requests = append(requests, body)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
			"id":"chat-1",
			"model":"anthropic/claude-sonnet",
			"choices":[{
				"index":0,
				"finish_reason":"stop",
				"message":{
					"role":"assistant",
					"content":"final answer",
					"reasoning":"visible text duplicate",
					"reasoning_details":[
						{"type":"reasoning.text","text":"step one","signature":"sig-1","id":"detail-1","format":"anthropic-claude-v1","index":0},
						{"type":"reasoning.encrypted","data":"opaque-data","id":"detail-2","format":"anthropic-claude-v1","index":1},
						{"type":"reasoning.summary","summary":"short summary","id":"detail-3","format":"anthropic-claude-v1","index":2}
					]
				}
			}]
		}`))
	}))
	t.Cleanup(server.Close)

	model, err := openrouter.NewChat(t.Context(), openrouter.ChatConfig{
		APIKey:         "test-key",
		BaseURL:        server.URL,
		DefaultOptions: corechat.Options{Model: "anthropic/claude-sonnet"},
	})
	if err != nil {
		t.Fatalf("NewChat: %v", err)
	}
	firstRequest := &corechat.Request{Messages: []corechat.Message{
		corechat.NewUserMessage(corechat.NewTextPart("solve it")),
	}}
	firstResponse, err := model.Call(t.Context(), firstRequest)
	if err != nil {
		t.Fatalf("first Call: %v", err)
	}
	message := firstResponse.Output.Message
	if message == nil || len(message.Parts) != 4 {
		t.Fatalf("response message = %#v", message)
	}
	if message.Parts[0].Text != "step one" || len(message.Parts[0].ReasoningState) == 0 {
		t.Errorf("text reasoning = %#v", message.Parts[0])
	}
	if message.Parts[1].Text != "" || len(message.Parts[1].ReasoningState) == 0 {
		t.Errorf("encrypted reasoning = %#v", message.Parts[1])
	}
	if message.Parts[2].Text != "short summary" || message.Parts[3].Text != "final answer" {
		t.Errorf("summary/answer = %#v", message.Parts)
	}

	secondRequest := &corechat.Request{Messages: []corechat.Message{
		firstRequest.Messages[0],
		message.Clone(),
		corechat.NewUserMessage(corechat.NewTextPart("continue")),
	}}
	if _, err := model.Call(t.Context(), secondRequest); err != nil {
		t.Fatalf("second Call: %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %d; want 2", len(requests))
	}
	secondMessages := requests[1]["messages"].([]any)
	assistant := secondMessages[1].(map[string]any)
	if _, found := assistant["reasoning"]; found {
		t.Fatalf("structured history unexpectedly downgraded to reasoning string: %#v", assistant)
	}
	details, ok := assistant["reasoning_details"].([]any)
	if !ok || len(details) != 3 {
		t.Fatalf("reasoning_details = %#v", assistant["reasoning_details"])
	}
	assertReasoningDetail(t, details[0], "reasoning.text", "text", "step one")
	assertReasoningDetail(t, details[1], "reasoning.encrypted", "data", "opaque-data")
	assertReasoningDetail(t, details[2], "reasoning.summary", "summary", "short summary")
}

func TestChatCoalescesStreamedReasoningDetailsForReplay(t *testing.T) {
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
			fmt.Fprint(writer, "data: {\"id\":\"stream-1\",\"model\":\"model\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_details\":[{\"type\":\"reasoning.text\",\"text\":\"step \",\"id\":\"detail-1\",\"format\":\"anthropic-claude-v1\",\"index\":0}]}}]}\n\n")
			fmt.Fprint(writer, "data: {\"id\":\"stream-1\",\"model\":\"model\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_details\":[{\"type\":\"reasoning.text\",\"text\":\"two\",\"id\":\"detail-1\",\"format\":\"anthropic-claude-v1\",\"index\":0}]}}]}\n\n")
			fmt.Fprint(writer, "data: {\"id\":\"stream-1\",\"model\":\"model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\n")
			fmt.Fprint(writer, "data: [DONE]\n\n")
			return
		}
		replayRequest = body
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"chat-2","model":"model","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"done"}}]}`))
	}))
	t.Cleanup(server.Close)

	model, err := openrouter.NewChat(t.Context(), openrouter.ChatConfig{
		APIKey:         "test-key",
		BaseURL:        server.URL,
		DefaultOptions: corechat.Options{Model: "model"},
	})
	if err != nil {
		t.Fatalf("NewChat: %v", err)
	}
	first := corechat.NewUserMessage(corechat.NewTextPart("solve"))
	var accumulator corechat.ResponseAccumulator
	for response, streamErr := range model.Stream(t.Context(), &corechat.Request{Messages: []corechat.Message{first}}) {
		if streamErr != nil {
			t.Fatalf("Stream: %v", streamErr)
		}
		if addErr := accumulator.Add(response); addErr != nil {
			t.Fatalf("accumulate: %v", addErr)
		}
	}
	aggregated, err := accumulator.Response()
	if err != nil {
		t.Fatalf("promote stream: %v", err)
	}
	if aggregated == nil || aggregated.Output == nil || aggregated.Output.Message == nil {
		t.Fatalf("aggregated response = %#v", aggregated)
	}
	message := aggregated.Output.Message
	if len(message.Parts) != 2 || message.Parts[0].Text != "step two" || message.Parts[1].Text != "answer" {
		t.Fatalf("aggregated parts = %#v", message.Parts)
	}
	if _, err := model.Call(t.Context(), &corechat.Request{Messages: []corechat.Message{
		first,
		message.Clone(),
		corechat.NewUserMessage(corechat.NewTextPart("continue")),
	}}); err != nil {
		t.Fatalf("replay Call: %v", err)
	}
	messages := replayRequest["messages"].([]any)
	details := messages[1].(map[string]any)["reasoning_details"].([]any)
	if len(details) != 1 {
		t.Fatalf("reasoning_details = %#v", details)
	}
	assertReasoningDetail(t, details[0], "reasoning.text", "text", "step two")
}

func assertReasoningDetail(t *testing.T, value any, wantType, payloadField, wantPayload string) {
	t.Helper()
	detail, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("reasoning detail = %#v", value)
	}
	if detail["type"] != wantType || detail[payloadField] != wantPayload {
		t.Errorf("reasoning detail = %#v; want type %q and %s %q", detail, wantType, payloadField, wantPayload)
	}
}
