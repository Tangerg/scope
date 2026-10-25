package fireworks_test

import (
	"fmt"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/fireworks"
)

func TestChatBindingPreservesReasoningAndFinalResponse(t *testing.T) {
	assertResponse := func(t *testing.T, response *chat.Response) {
		t.Helper()
		reasoning := ""
		for _, part := range response.Output.Message.Parts {
			if part.Kind == chat.PartReasoning {
				reasoning += part.Text
			}
		}
		if response.Text() != "answer" || reasoning != "basis" {
			t.Fatalf("response lost content or reasoning: %+v", response)
		}
	}
	modeltest.RunChatContract(t, modeltest.ChatContract{
		New: func(t *testing.T) (chat.Model, chat.Streamer) {
			server := modeltest.OpenAISSEServer([]string{
				fmt.Sprintf(`{"id":"completion-1","object":"chat.completion.chunk","model":%q,"choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"basis","content":"answer"}}]}`, fireworks.ModelGPTOSS120B),
				fmt.Sprintf(`{"id":"completion-1","object":"chat.completion.chunk","model":%q,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, fireworks.ModelGPTOSS120B),
			})
			t.Cleanup(server.Close)
			model, err := fireworks.NewChatCompletions(t.Context(), fireworks.ChatCompletionsConfig{
				APIKey: "test-key", BaseURL: server.URL, DefaultOptions: chat.Options{Model: fireworks.ModelGPTOSS120B},
			})
			if err != nil {
				t.Fatal(err)
			}
			return model, model
		},
		Request: func(t *testing.T) *chat.Request {
			request, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("question")))
			if err != nil {
				t.Fatal(err)
			}
			return request
		},
		AssertCall:       assertResponse,
		AssertAggregated: assertResponse,
	})
}

func TestChatConstructionRejectsInvalidConfiguration(t *testing.T) {
	for _, config := range []fireworks.ChatCompletionsConfig{
		{},
		{APIKey: "test-key", DefaultOptions: chat.Options{MaxOutputTokens: new(int64(-1))}},
		{APIKey: "test-key", BaseURL: "://"},
	} {
		if model, err := fireworks.NewChatCompletions(t.Context(), config); err == nil || model != nil {
			t.Fatalf("invalid configuration constructed a model: %v", err)
		}
	}
}
