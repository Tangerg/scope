package openai_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/models/protocol/openai"
)

func TestUnsupportedOutputSchemaDoesNotDispatch(t *testing.T) {
	boundary := new(numericTransport)
	client := &http.Client{Transport: boundary}
	chatModel, err := openai.NewChatCompletions(t.Context(), openai.ChatCompletionsConfig{
		APIKey: "test", BaseURL: "https://provider.example.test", HTTPClient: client,
		DefaultOptions: chat.Options{Model: "test"},
	}, testChatDialect())
	if err != nil {
		t.Fatal(err)
	}
	responsesModel, err := openai.NewResponses(t.Context(), openai.ResponsesConfig{
		APIKey: "test", BaseURL: "https://provider.example.test", HTTPClient: client,
		DefaultOptions: chat.Options{Model: "test"},
	}, testResponsesDialect())
	if err != nil {
		t.Fatal(err)
	}
	format, err := chat.NewJSONSchemaOutputFormat(chat.JSONSchemaConfig{
		Name: "result", Schema: json.RawMessage(`{"type":"object","properties":{"optional":{"type":"string"}},"additionalProperties":false}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("answer")))
	if err != nil {
		t.Fatal(err)
	}
	request.Options.OutputFormat = &format
	for _, model := range []interface {
		chat.Model
		chat.Streamer
	}{chatModel, responsesModel} {
		_, callErr := model.Call(t.Context(), request)
		if !errors.Is(callErr, chat.ErrUnsupportedOutputFormat) || invocation.ClassifyFailure(callErr) != invocation.FailureNotExecuted {
			t.Fatalf("%T.Call: %v", model, callErr)
		}
		var streamErr error
		for delta, err := range model.Stream(t.Context(), request) {
			if delta != nil {
				t.Fatalf("%T.Stream emitted before admission: %#v", model, delta)
			}
			streamErr = err
		}
		if !errors.Is(streamErr, chat.ErrUnsupportedOutputFormat) || invocation.ClassifyFailure(streamErr) != invocation.FailureNotExecuted {
			t.Fatalf("%T.Stream: %v", model, streamErr)
		}
	}
	_, countErr := responsesModel.CountInputTokens(t.Context(), request)
	if !errors.Is(countErr, chat.ErrUnsupportedOutputFormat) || boundary.calls != 0 {
		t.Fatalf("CountInputTokens = %v, requests = %d", countErr, boundary.calls)
	}
}
