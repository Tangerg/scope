package openai_test

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/protocol/openai"
)

func TestDialectBindingOwnsNamespaceAndReasoningIssuer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Marker   string           `json:"marker"`
			Messages []map[string]any `json:"messages"`
		}
		if err := jsonv2.UnmarshalRead(r.Body, &request); err != nil {
			t.Error(err)
		}
		field := "reasoning_content"
		if request.Marker == "second" {
			field = "reasoning"
		}
		if request.Messages[1][field] != request.Marker+" history" {
			t.Errorf("issuer %q replay=%v", request.Marker, request.Messages[1])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"id\":\"response\",\"model\":\"model\",\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"delta\":{\"content\":\"answer\",%q:\"new reasoning\"}}]}\n\ndata: [DONE]\n\n", field)
	}))
	t.Cleanup(server.Close)
	fields := []openai.TextReasoningField{openai.TextReasoningContent}
	dialect := openai.DialectConfig{Provider: "first", TokenLimitField: openai.TokenLimitMaxTokens, TextReasoning: &openai.TextReasoningConfig{Fields: fields, Replay: openai.TextReasoningReplayAlways}}
	config := openai.ChatCompletionsConfig{APIKey: "key", BaseURL: server.URL, DefaultOptions: chat.Options{Model: "model"}}
	first, err := openai.NewChatCompletions(t.Context(), config, dialect)
	if err != nil {
		t.Fatal(err)
	}
	dialect.Provider = "second"
	fields[0] = openai.TextReasoning
	second, err := openai.NewChatCompletions(t.Context(), config, dialect)
	if err != nil {
		t.Fatal(err)
	}
	dialect.Provider = "changed"
	fields[0] = "invalid caller mutation"
	for _, binding := range []struct {
		issuer string
		field  openai.TextReasoningField
		model  *openai.ChatCompletions
	}{
		{"first", openai.TextReasoningContent, first}, {"second", openai.TextReasoning, second},
	} {
		t.Run(binding.issuer, func(t *testing.T) {
			prior, err := openai.NewTextReasoningPart(binding.issuer, binding.field, binding.issuer+" history")
			if err != nil {
				t.Fatal(err)
			}
			request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello")), chat.NewAssistantMessage(prior, chat.NewTextPart("previous"))}}
			if extensionErr := request.Options.Extensions.Set(binding.issuer+"/openai_request", map[string]any{"marker": binding.issuer}); extensionErr != nil {
				t.Fatal(extensionErr)
			}
			response, err := binding.model.Call(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			issuer, found, err := response.Output.Message.Parts[0].Metadata.Decode[string]("openai/reasoning_provider")
			if err != nil || !found || issuer != binding.issuer {
				t.Fatalf("reasoning issuer=%q found=%v error=%v", issuer, found, err)
			}
			if _, found, err := response.Metadata.Extra.Decode[map[string]any](binding.issuer + "/openai_stream_chunk"); err != nil || !found {
				t.Fatalf("response namespace missing: found=%v error=%v", found, err)
			}
		})
	}
}
