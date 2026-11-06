package openrouter_test

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/openrouter"
)

func TestChatMapsCoreTopK(t *testing.T) {
	captured := make(chan int64, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			TopK int64 `json:"top_k"`
		}
		if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
			t.Error(err)
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		captured <- body.TopK
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"id\":\"chat\",\"model\":\"model\",\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"delta\":{\"content\":\"answer\"}}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)
	model, err := openrouter.NewChatCompletions(t.Context(), openrouter.ChatCompletionsConfig{APIKey: "test-key", BaseURL: server.URL, DefaultOptions: chat.Options{Model: "model", TopK: new(int64(40))}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := model.Call(t.Context(), &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}, Options: chat.Options{TopK: new(int64(7))}}); err != nil {
		t.Fatal(err)
	}
	if got := <-captured; got != 7 {
		t.Fatalf("top_k=%d, want request override 7", got)
	}
}
