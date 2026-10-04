package minimax_test

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/minimax"
)

func TestChatReplaysExplicitEmptyReasoningDetails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			Messages []map[string]any `json:"messages"`
		}
		if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		if len(body.Messages) > 1 {
			details, present := body.Messages[1]["reasoning_details"].([]any)
			if !present || len(details) != 0 {
				http.Error(writer, "assistant history must preserve reasoning_details: []", http.StatusBadRequest)
				return
			}
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"id\":\"chat-1\",\"model\":\"model\",\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"delta\":{\"role\":\"assistant\",\"content\":\"answer\",\"reasoning_details\":[]}}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)
	model, err := minimax.NewChatCompletions(t.Context(), minimax.ChatCompletionsConfig{
		APIKey: "test-key", BaseURL: server.URL,
		DefaultOptions: corechat.Options{Model: "model"},
	})
	if err != nil {
		t.Fatal(err)
	}
	user := corechat.NewUserMessage(corechat.NewTextPart("solve it"))
	response, err := model.Call(t.Context(), &corechat.Request{Messages: []corechat.Message{user}})
	if err != nil {
		t.Fatalf("first Call: %v", err)
	}
	if response.Output.Message == nil {
		t.Fatal("first Call produced no assistant message")
	}
	raw, err := response.Output.Message.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var assistant corechat.Message
	if decodeErr := assistant.UnmarshalJSON(raw); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	_, err = model.Call(t.Context(), &corechat.Request{Messages: []corechat.Message{
		user, assistant, corechat.NewUserMessage(corechat.NewTextPart("continue")),
	}})
	if err != nil {
		t.Fatalf("replay of an explicit empty reasoning_details array: %v", err)
	}
}
