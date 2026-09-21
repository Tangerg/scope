package azureopenai_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/azureopenai"
)

func TestChatUsesAzureOpenAIV1Protocol(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/openai/v1/chat/completions" {
			t.Errorf("path = %q; want /openai/v1/chat/completions", request.URL.Path)
		}
		if request.URL.RawQuery != "" {
			t.Errorf("query = %q; want empty", request.URL.RawQuery)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q; want Bearer test-key", got)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(writer, "data: %s\n\ndata: [DONE]\n\n", `{"id":"chat-1","object":"chat.completion.chunk","model":"gpt-deployment","choices":[{"index":0,"finish_reason":"stop","delta":{"role":"assistant","content":"ok"}}]}`)
	}))
	t.Cleanup(server.Close)

	model, err := azureopenai.NewChat(t.Context(), azureopenai.ChatConfig{
		Config: azureopenai.Config{APIKey: "test-key", BaseURL: server.URL + "/openai/v1/"},
		DefaultOptions: corechat.Options{
			Model: "gpt-deployment",
		},
	})
	if err != nil {
		t.Fatalf("NewChat: %v", err)
	}
	request := &corechat.Request{Messages: []corechat.Message{
		corechat.NewUserMessage(corechat.NewTextPart("hello")),
	}}
	if _, err := model.Call(t.Context(), request); err != nil {
		t.Fatalf("Call: %v", err)
	}
}
