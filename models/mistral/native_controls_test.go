package mistral_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/models/mistral"
)

func TestEmbeddingUsesMistralDimensionField(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]json.RawMessage
		if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
			t.Error(err)
			return
		}
		if string(body["output_dimension"]) != "3" || len(body["dimensions"]) != 0 {
			t.Errorf("dimension projection = %s; OpenAI dimensions = %s", body["output_dimension"], body["dimensions"])
		}
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, mistralEmbedResponseJSON)
	}))
	t.Cleanup(server.Close)
	model, err := mistral.NewEmbeddingModel(t.Context(), mistral.EmbeddingModelConfig{
		APIKey: "test-key", BaseURL: server.URL,
		DefaultOptions: embedding.Options{Model: mistral.ModelCodestralEmbed, Dimensions: new(int64(3))},
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := embedding.NewRequest([]string{"foo", "bar"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := model.Call(t.Context(), request); err != nil {
		t.Fatal(err)
	}
}

func TestChatMapsNamedToolChoiceAndTopK(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			TopK              *int64 `json:"top_k"`
			ParallelToolCalls *bool  `json:"parallel_tool_calls"`
			ToolChoice        struct {
				Type     string `json:"type"`
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tool_choice"`
		}
		if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
			t.Error(err)
			return
		}
		if body.TopK == nil || *body.TopK != 7 || body.ParallelToolCalls == nil || *body.ParallelToolCalls || body.ToolChoice.Type != "function" || body.ToolChoice.Function.Name != "lookup" {
			t.Errorf("native controls = %#v", body)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"id\":\"chat\",\"model\":\"mistral-small-latest\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)
	model, err := mistral.NewChat(t.Context(), mistral.ChatConfig{
		APIKey: "test-key", BaseURL: server.URL, DefaultOptions: chat.Options{Model: mistral.ModelSmall},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := &chat.Request{
		Messages:   []chat.Message{chat.NewUserMessage(chat.NewTextPart("look up"))},
		Tools:      []chat.ToolDefinition{{Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		ToolChoice: &chat.ToolChoice{Mode: chat.ToolChoiceNamed, Name: "lookup", Parallelism: chat.ToolParallelismSingle},
		Options:    chat.Options{TopK: new(int64(7))},
	}
	if _, err := model.Call(t.Context(), request); err != nil {
		t.Fatal(err)
	}
}
