package huggingface_test

import (
	jsonv2 "encoding/json/v2"
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/huggingface"
)

func TestChatBindingPreservesRequestAndTerminalResponse(t *testing.T) {
	const stream = "data: {\"id\":\"reply\",\"model\":\"test-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"}}]}\n\n" +
		"data: {\"id\":\"reply\",\"model\":\"test-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	assertResponse := func(t *testing.T, response *chat.Response) {
		t.Helper()
		if response.Text() != "answer" || response.Output.FinishReason != chat.FinishReasonStop {
			t.Fatalf("response=%#v", response)
		}
	}
	modeltest.RunChatContract(t, modeltest.ChatContract{
		New: func(t *testing.T) (chat.Model, chat.Streamer) {
			t.Helper()
			server := modeltest.BinaryServer(http.StatusOK, "text/event-stream", []byte(stream), func(request *http.Request) {
				var body struct {
					Model               string `json:"model"`
					MaxTokens           int64  `json:"max_tokens"`
					MaxCompletionTokens *int64 `json:"max_completion_tokens"`
					Stream              bool   `json:"stream"`
				}
				if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
					t.Error(err)
					return
				}
				if request.URL.Path != "/chat/completions" || request.Header.Get("Authorization") != "Bearer test" || body.Model != "test-model" || body.MaxTokens != 13 || body.MaxCompletionTokens != nil || !body.Stream {
					t.Errorf("request=%s body=%#v", request.URL.Path, body)
				}
			})
			t.Cleanup(server.Close)
			model, err := huggingface.NewChatCompletions(t.Context(), huggingface.ChatCompletionsConfig{
				APIKey: "test", BaseURL: server.URL, DefaultOptions: chat.Options{Model: "test-model"},
			})
			if err != nil {
				t.Fatal(err)
			}
			return model, model
		},
		Request: func(t *testing.T) *chat.Request {
			return &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("question"))}, Options: chat.Options{MaxOutputTokens: new(int64(13))}}
		},
		AssertCall: assertResponse, AssertAggregated: assertResponse,
	})
}

func TestChatConstructionRejectsInvalidConfiguration(t *testing.T) {
	for _, config := range []huggingface.ChatCompletionsConfig{
		{APIKey: "test", DefaultOptions: chat.Options{MaxOutputTokens: new(int64(-1))}},
		{APIKey: "test", BaseURL: "://"},
	} {
		if model, err := huggingface.NewChatCompletions(t.Context(), config); model != nil || err == nil {
			t.Fatalf("invalid configuration constructed a model: %#v, %v", model, err)
		}
	}
}
