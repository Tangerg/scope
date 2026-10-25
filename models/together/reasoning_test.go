package together_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/protocol/openai"
	"github.com/Tangerg/scope/models/together"
)

func TestReasoningFieldsKeepTheirReplayIdentity(t *testing.T) {
	bodies := make(chan map[string]any, 4)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
			t.Error(err)
			return
		}
		bodies <- body
		field := body["model"].(string)
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(writer, "data: {\"id\":\"reply\",\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{%q:\"thinking\",\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", field, field)
	}))
	defer server.Close()
	model, err := together.NewChatCompletions(t.Context(), together.ChatCompletionsConfig{APIKey: "test", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []openai.TextReasoningField{openai.TextReasoning, openai.TextReasoningContent} {
		request := &corechat.Request{Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("solve"))}, Options: corechat.Options{Model: string(field)}}
		response, err := model.Call(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		<-bodies
		parts := response.Output.Message.Parts
		if len(parts) != 2 || parts[0].Kind != corechat.PartReasoning || parts[0].Text != "thinking" || parts[1].Text != "answer" {
			t.Fatalf("%s response parts = %#v", field, parts)
		}
		request.Messages = append(request.Messages, *response.Output.Message, corechat.NewUserMessage(corechat.NewTextPart("continue")))
		if _, err := model.Call(t.Context(), request); err != nil {
			t.Fatal(err)
		}
		body := <-bodies
		assistant := body["messages"].([]any)[1].(map[string]any)
		if assistant[string(field)] != "thinking" {
			t.Fatalf("%s replay = %#v", field, assistant)
		}
		for _, other := range []openai.TextReasoningField{openai.TextReasoning, openai.TextReasoningContent} {
			if _, exists := assistant[string(other)]; other != field && exists {
				t.Fatalf("unexpected replay field %s: %#v", other, assistant)
			}
		}
	}
}

func TestReasoningFieldCannotChangeWithinOneReply(t *testing.T) {
	for _, deltas := range [][]string{
		{`{"reasoning":"one","reasoning_content":"two"}`},
		{`{"reasoning":"one"}`, `{"reasoning_content":"two"}`},
	} {
		t.Run(fmt.Sprint(len(deltas)), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "text/event-stream")
				for _, delta := range deltas {
					fmt.Fprintf(writer, "data: {\"id\":\"reply\",\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":%s}]}\n\n", delta)
				}
				fmt.Fprint(writer, "data: {\"id\":\"reply\",\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()
			model, err := together.NewChatCompletions(t.Context(), together.ChatCompletionsConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: corechat.Options{Model: "test"}})
			if err != nil {
				t.Fatal(err)
			}
			request := &corechat.Request{Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hello"))}}
			response, err := model.Call(t.Context(), request)
			if response != nil || !errors.Is(err, corechat.ErrInvalidResponse) {
				t.Fatalf("Call = %v, %v", response, err)
			}
			failed := false
			for delta, err := range model.Stream(t.Context(), request) {
				if err != nil {
					failed = errors.Is(err, corechat.ErrInvalidResponse)
					continue
				}
				if delta.FinishReason != "" {
					t.Fatal("contradictory reasoning completed successfully")
				}
			}
			if !failed {
				t.Fatal("missing invalid-response error")
			}
		})
	}
}

func TestReasoningReplayRejectsMixedFieldOwners(t *testing.T) {
	first, err := openai.NewTextReasoningPart("together", openai.TextReasoning, "first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := openai.NewTextReasoningPart("together", openai.TextReasoningContent, "second")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("invalid replay reached provider")
		http.Error(w, "invalid replay", http.StatusBadRequest)
	}))
	defer server.Close()
	model, err := together.NewChatCompletions(t.Context(), together.ChatCompletionsConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: corechat.Options{Model: "test"}})
	if err != nil {
		t.Fatal(err)
	}
	response, err := model.Call(t.Context(), &corechat.Request{Messages: []corechat.Message{
		corechat.NewUserMessage(corechat.NewTextPart("hello")), corechat.NewAssistantMessage(first, second, corechat.NewTextPart("answer")), corechat.NewUserMessage(corechat.NewTextPart("next")),
	}})
	if response != nil || corechat.ClassifyFailure(err) != corechat.CallFailureNotExecuted {
		t.Fatalf("Call = %v, %v", response, err)
	}
}
