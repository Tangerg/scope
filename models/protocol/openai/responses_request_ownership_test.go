package openai_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/protocol/openai"
)

func TestResponsesInstructionsBelongToCoreMessages(t *testing.T) {
	for _, native := range []any{"second guidance", "", nil} {
		for _, system := range []bool{false, true} {
			for _, dialect := range []bool{false, true} {
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
				config := openai.ResponsesConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: chat.Options{Model: "model"}}
				binding := openai.ResponsesDialect{Provider: "openai"}
				if dialect {
					binding.PrepareRequest = func(_ *chat.Request, target *openai.CompatibleResponsesRequest) error {
						return target.SetExtraField("instructions", native)
					}
				}
				model, err := openai.NewCompatibleResponses(t.Context(), config, binding)
				if err != nil {
					t.Fatal(err)
				}
				request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("question"))}}
				if system {
					request.Messages = append([]chat.Message{chat.NewSystemMessage("guidance")}, request.Messages...)
				}
				if !dialect {
					if err := request.Options.Extensions.Set(openai.ResponsesRequestExtensionKey, map[string]any{"instructions": native}); err != nil {
						t.Fatal(err)
					}
				}
				response, callErr := model.Call(t.Context(), request)
				_, countErr := model.CountInputTokens(t.Context(), request)
				server.Close()
				if response != nil || chat.ClassifyFailure(callErr) != chat.CallFailureNotExecuted || countErr == nil || calls != 0 {
					t.Fatalf("native=%v system=%v dialect=%v: response=%v Call=%v CountInputTokens=%v calls=%d", native, system, dialect, response, callErr, countErr, calls)
				}
			}
		}
	}
}

func TestResponsesProjectsSystemGuidanceAndNativeControls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var wire struct {
			Instructions json.RawMessage `json:"instructions"`
			ServiceTier  string          `json:"service_tier"`
			Input        []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"input"`
		}
		if err := jsonv2.UnmarshalRead(request.Body, &wire); err != nil {
			t.Error(err)
		}
		if len(wire.Instructions) != 0 || wire.ServiceTier != "flex" || len(wire.Input) != 2 || wire.Input[0].Role != "system" || string(wire.Input[0].Content) != `"guidance"` {
			t.Errorf("wire=%+v", wire)
		}
		writeResponsesCompletion(writer)
	}))
	defer server.Close()
	model := newResponsesModel(t, server.URL, "model")
	request := &chat.Request{Messages: []chat.Message{chat.NewSystemMessage("guidance"), chat.NewUserMessage(chat.NewTextPart("question"))}}
	if err := request.Options.Extensions.Set(openai.ResponsesRequestExtensionKey, map[string]any{"service_tier": "flex"}); err != nil {
		t.Fatal(err)
	}
	if _, err := model.Call(t.Context(), request); err != nil {
		t.Fatal(err)
	}
}
