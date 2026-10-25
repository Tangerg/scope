package groq_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/groq"
)

func TestSchemaFormatIsRejectedBeforeStreaming(t *testing.T) {
	calls := 0
	server := modeltest.JSONServer(http.StatusBadRequest, `{}`, func(*http.Request) { calls++ })
	defer server.Close()
	model, err := groq.NewChatCompletions(t.Context(), groq.ChatCompletionsConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: chat.Options{Model: "openai/gpt-oss-20b"}})
	if err != nil {
		t.Fatal(err)
	}
	format, err := chat.NewJSONSchemaOutputFormat(chat.JSONSchemaConfig{Name: "answer", Schema: json.RawMessage(`{"type":"object"}`)})
	if err != nil {
		t.Fatal(err)
	}
	request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("answer"))}, Options: chat.Options{OutputFormat: &format}}
	_, err = model.Call(t.Context(), request)
	if !errors.Is(err, chat.ErrUnsupportedOutputFormat) || chat.ClassifyFailure(err) != chat.CallFailureNotExecuted || calls != 0 {
		t.Fatalf("error=%v calls=%d", err, calls)
	}
}

func TestJSONObjectFormatRemainsAvailable(t *testing.T) {
	server := modeltest.OpenAISSEServer([]string{`{"id":"response-1","model":"model","choices":[{"index":0,"delta":{"content":"{}"},"finish_reason":"stop"}]}`})
	defer server.Close()
	model, err := groq.NewChatCompletions(t.Context(), groq.ChatCompletionsConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: chat.Options{Model: "openai/gpt-oss-20b"}})
	if err != nil {
		t.Fatal(err)
	}
	request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("answer in JSON"))}, Options: chat.Options{OutputFormat: &chat.OutputFormat{Type: chat.OutputFormatJSON}}}
	response, err := model.Call(t.Context(), request)
	if err != nil || response.Output.Message.Text() != "{}" {
		t.Fatalf("response=%v error=%v", response, err)
	}
}
