package openai_test

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/protocol/openai"
)

func TestReasoningDetailsPreserveArrayPresence(t *testing.T) {
	const empty = `"reasoning_details":[]`
	const text = `"reasoning_details":[{"type":"reasoning.text","text":"thinking","id":"detail-1"}]`
	wantText := []any{map[string]any{"type": "reasoning.text", "text": "thinking", "id": "detail-1"}}
	for _, test := range []struct {
		name           string
		chunks         []string
		replayProvider string
		want           []any
	}{
		{name: "absent", chunks: []string{""}},
		{name: "null", chunks: []string{`"reasoning_details":null`}},
		{name: "empty", chunks: []string{empty}, want: []any{}},
		{name: "repeated empty", chunks: []string{empty, empty}, want: []any{}},
		{name: "empty then text", chunks: []string{empty, text}, want: wantText},
		{name: "text then empty", chunks: []string{text, empty}, want: wantText},
		{name: "empty with duplicate plain field", chunks: []string{empty + `,"reasoning":"duplicate"`}, want: []any{}},
		{name: "empty is provider scoped", chunks: []string{empty}, replayProvider: "other"},
	} {
		t.Run(test.name, func(t *testing.T) {
			captured := make(chan []map[string]json.RawMessage, 2)
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				var body struct {
					Messages []map[string]json.RawMessage `json:"messages"`
				}
				if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
					t.Error(err)
					http.Error(writer, "invalid request", http.StatusBadRequest)
					return
				}
				captured <- body.Messages
				writer.Header().Set("Content-Type", "text/event-stream")
				for _, fields := range test.chunks {
					fmt.Fprintf(writer, "data: {\"id\":\"chat-1\",\"model\":\"model\",\"choices\":[{\"index\":0,\"delta\":{%s}}]}\n\n", fields)
				}
				fmt.Fprint(writer, "data: {\"id\":\"chat-1\",\"model\":\"model\",\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"delta\":{\"content\":\"answer\"}}]}\n\ndata: [DONE]\n\n")
			}))
			t.Cleanup(server.Close)
			config := openai.ChatCompletionsConfig{
				APIKey: "test-key", BaseURL: server.URL, DefaultOptions: chat.Options{Model: "model"},
			}
			dialect, err := openai.ReasoningDetailsDialect(openai.ReasoningDetailsConfig{
				Provider: "provider", TextField: "reasoning", DetailsField: "reasoning_details", ReplayPlainText: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			model, err := openai.NewCompatibleChatCompletions(t.Context(), config, dialect)
			if err != nil {
				t.Fatal(err)
			}
			user := chat.NewUserMessage(chat.NewTextPart("question"))
			response, err := model.Call(t.Context(), &chat.Request{Messages: []chat.Message{user}})
			if err != nil {
				t.Fatal(err)
			}
			<-captured
			if response.Output.Message == nil {
				t.Fatal("response has no assistant message")
			}
			raw, err := response.Output.Message.MarshalJSON()
			if err != nil {
				t.Fatal(err)
			}
			var assistant chat.Message
			if decodeErr := assistant.UnmarshalJSON(raw); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			if test.replayProvider != "" {
				dialect, err = openai.ReasoningDetailsDialect(openai.ReasoningDetailsConfig{
					Provider: test.replayProvider, TextField: "reasoning", DetailsField: "reasoning_details", ReplayPlainText: true,
				})
				if err != nil {
					t.Fatal(err)
				}
				model, err = openai.NewCompatibleChatCompletions(t.Context(), config, dialect)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := model.Call(t.Context(), &chat.Request{Messages: []chat.Message{user, assistant}}); err != nil {
				t.Fatalf("replay: %v", err)
			}
			messages := <-captured
			if len(messages) != 2 {
				t.Fatalf("replayed messages = %d, want 2", len(messages))
			}
			details, present := messages[1]["reasoning_details"]
			if present != (test.want != nil) {
				t.Fatalf("reasoning_details presence = %v, want %v; assistant = %v", present, test.want != nil, messages[1])
			}
			if present {
				var got []any
				if err := jsonv2.Unmarshal(details, &got); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, test.want) {
					t.Fatalf("reasoning_details = %#v, want %#v", got, test.want)
				}
			}
			if _, present := messages[1]["reasoning"]; present {
				t.Fatal("structured history replayed a competing plain reasoning field")
			}
			if bytes.Contains(raw, []byte("duplicate")) {
				t.Fatal("duplicate plain reasoning became message state")
			}
		})
	}
}
