package openai_test

import (
	"bytes"
	"encoding/base64"
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/protocol/openai"
)

func TestReasoningDetailsConfigValidate(t *testing.T) {
	valid := openai.ReasoningDetailsConfig{
		Provider:     "provider",
		TextField:    "reasoning",
		DetailsField: "reasoning_details",
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if err := (openai.ReasoningDetailsConfig{}).Validate(); err == nil {
		t.Fatal("zero ReasoningDetailsConfig.Validate() error = nil")
	}
	for _, field := range []string{"role", "content", "tool_calls", "function_call", "name", "refusal", "audio", "reasoning", " reasoning_details"} {
		invalid := valid
		invalid.DetailsField = field
		if err := invalid.Validate(); err == nil {
			t.Errorf("Validate accepted competing or invalid detail field %q", field)
		}
	}
}

func TestReasoningDetailsReplayUsesCoreText(t *testing.T) {
	for _, test := range []struct{ kind, field string }{
		{"reasoning.text", "text"},
		{"reasoning.summary", "summary"},
	} {
		t.Run(test.kind, func(t *testing.T) {
			var captured struct {
				Messages []struct {
					Details []map[string]any `json:"reasoning_details"`
				} `json:"messages"`
			}
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if err := jsonv2.UnmarshalRead(request.Body, &captured); err != nil {
					t.Error(err)
					http.Error(writer, "invalid request", http.StatusBadRequest)
					return
				}
				writer.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(writer, "data: {\"id\":\"chat-1\",\"model\":\"model\",\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"delta\":{\"content\":\"answer\",\"reasoning_details\":[{\"type\":%q,%q:\"original reasoning\",\"id\":\"detail-1\",\"index\":0,\"signature\":\"signature\",\"vendor_field\":7}]}}]}\n\ndata: [DONE]\n\n", test.kind, test.field)
			}))
			t.Cleanup(server.Close)
			dialect, err := openai.ReasoningDetailsDialect(openai.ReasoningDetailsConfig{
				Provider: "provider", TextField: "reasoning", DetailsField: "reasoning_details",
			})
			if err != nil {
				t.Fatal(err)
			}
			model, err := openai.NewCompatibleChatCompletions(t.Context(), openai.ChatCompletionsConfig{
				APIKey: "test-key", BaseURL: server.URL, DefaultOptions: chat.Options{Model: "model"},
			}, dialect)
			if err != nil {
				t.Fatal(err)
			}
			user := chat.NewUserMessage(chat.NewTextPart("question"))
			response, err := model.Call(t.Context(), &chat.Request{Messages: []chat.Message{user}})
			if err != nil {
				t.Fatal(err)
			}
			message := response.Output.Message.Clone()
			if len(message.Parts) != 2 || message.Parts[0].Text != "original reasoning" {
				t.Fatalf("reasoning response = %#v", message)
			}
			message.Parts[0].Text = "edited reasoning"
			encoded, err := jsonv2.Marshal(message)
			if err != nil {
				t.Fatal(err)
			}
			if err := jsonv2.Unmarshal(encoded, &message); err != nil {
				t.Fatal(err)
			}
			if _, err := model.Call(t.Context(), &chat.Request{Messages: []chat.Message{user, message}}); err != nil {
				t.Fatal(err)
			}
			details := captured.Messages[1].Details
			if len(details) != 1 || details[0][test.field] != "edited reasoning" || details[0]["type"] != test.kind || details[0]["id"] != "detail-1" || details[0]["signature"] != "signature" || details[0]["vendor_field"] != float64(7) {
				t.Fatalf("replayed details = %#v, want current Core text with native fields", details)
			}
			if bytes.Contains(message.Parts[0].ReasoningState, []byte("original reasoning")) {
				t.Fatal("replay state retains a second copy of Core reasoning text")
			}
		})
	}
}

func TestReasoningDetailsRejectStoredTextCopies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("invalid reasoning state reached the provider")
	}))
	t.Cleanup(server.Close)
	dialect, err := openai.ReasoningDetailsDialect(openai.ReasoningDetailsConfig{
		Provider: "provider", TextField: "reasoning", DetailsField: "reasoning_details",
	})
	if err != nil {
		t.Fatal(err)
	}
	model, err := openai.NewCompatibleChatCompletions(t.Context(), openai.ChatCompletionsConfig{
		APIKey: "test-key", BaseURL: server.URL, DefaultOptions: chat.Options{Model: "model"},
	}, dialect)
	if err != nil {
		t.Fatal(err)
	}
	state, err := base64.StdEncoding.DecodeString("TFlSRAAIAAAARXByb3ZpZGVyeyJ0eXBlIjoicmVhc29uaW5nLnRleHQiLCJ0ZXh0Ijoib3JpZ2luYWwgcmVhc29uaW5nIiwiaWQiOiJkZXRhaWwtMSJ9")
	if err != nil {
		t.Fatal(err)
	}
	request := &chat.Request{Messages: []chat.Message{
		chat.NewUserMessage(chat.NewTextPart("question")),
		chat.NewAssistantMessage(chat.NewReasoningPart("edited reasoning", state)),
	}}
	if _, err := model.Call(t.Context(), request); err == nil {
		t.Fatal("replayed state carrying a second text owner")
	}
}

func TestReasoningDetailsKeepAnonymousStreamBoundaries(t *testing.T) {
	t.Run("omitted identity", func(t *testing.T) { testAnonymousReasoningBoundaries(t, "") })
	t.Run("null identity", func(t *testing.T) { testAnonymousReasoningBoundaries(t, `,"id":null,"index":null`) })
}

func testAnonymousReasoningBoundaries(t *testing.T, identity string) {
	t.Helper()
	var captured struct {
		Messages []struct {
			Details []map[string]any `json:"reasoning_details"`
		} `json:"messages"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := jsonv2.UnmarshalRead(request.Body, &captured); err != nil {
			t.Error(err)
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		for range 2 {
			fmt.Fprintf(writer, "data: {\"id\":\"chat-1\",\"model\":\"model\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_details\":[{\"type\":\"reasoning.text\",\"text\":\"same anonymous text\"%s}]}}]}\n\n", identity)
		}
		fmt.Fprint(writer, "data: {\"id\":\"chat-1\",\"model\":\"model\",\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"delta\":{\"content\":\"answer\"}}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)
	dialect, err := openai.ReasoningDetailsDialect(openai.ReasoningDetailsConfig{
		Provider: "provider", TextField: "reasoning", DetailsField: "reasoning_details",
	})
	if err != nil {
		t.Fatal(err)
	}
	model, err := openai.NewCompatibleChatCompletions(t.Context(), openai.ChatCompletionsConfig{
		APIKey: "test-key", BaseURL: server.URL, DefaultOptions: chat.Options{Model: "model"},
	}, dialect)
	if err != nil {
		t.Fatal(err)
	}
	user := chat.NewUserMessage(chat.NewTextPart("question"))
	response, err := model.Call(t.Context(), &chat.Request{Messages: []chat.Message{user}})
	if err != nil {
		t.Fatal(err)
	}
	parts := response.Output.Message.Parts
	if len(parts) != 3 || parts[0].Text != "same anonymous text" || parts[1].Text != "same anonymous text" {
		t.Fatalf("anonymous blocks lost their boundaries: %#v", parts)
	}
	if _, err := model.Call(t.Context(), &chat.Request{Messages: []chat.Message{user, response.Output.Message.Clone()}}); err != nil {
		t.Fatal(err)
	}
	details := captured.Messages[1].Details
	if len(details) != 2 {
		t.Fatalf("anonymous blocks = %#v, want two unchanged blocks", details)
	}
	for _, detail := range details {
		wantFields := 2
		if identity != "" {
			wantFields = 4
		}
		if len(detail) != wantFields || detail["type"] != "reasoning.text" || detail["text"] != "same anonymous text" || detail["id"] != nil || detail["index"] != nil {
			t.Fatalf("anonymous detail changed: %#v", detail)
		}
	}
}
