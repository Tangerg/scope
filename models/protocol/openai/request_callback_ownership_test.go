package openai_test

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/models/protocol/openai"
)

func TestPrepareRequestReceivesOwnedSnapshot(t *testing.T) {
	callbackFailure := errors.New("callback stopped")
	for _, protocol := range []string{"chat", "responses"} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/error=%t", protocol, fail), func(t *testing.T) {
				request := &chat.Request{
					Messages: []chat.Message{{
						Role: chat.RoleUser, Parts: []chat.Part{chat.NewTextPart("caller text")}, Metadata: metadata.Map{"label": json.RawMessage(`"caller"`)},
					}},
					Tools:      []tool.Definition{{Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)}},
					ToolChoice: &chat.ToolChoice{Mode: chat.ToolChoiceNamed, Name: "lookup"},
					Options:    chat.Options{MaxOutputTokens: new(int64(16))},
				}
				before, err := jsonv2.Marshal(request, jsonv2.Deterministic(true))
				if err != nil {
					t.Fatal(err)
				}
				mutate := func(source *chat.Request) error {
					if source.Options.Model != "model" {
						t.Errorf("callback model = %q, want resolved default", source.Options.Model)
					}
					source.Messages[0].Parts[0].Text = "callback text"
					source.Messages[0].Metadata["label"] = json.RawMessage(`"callback"`)
					source.Tools[0].Name = "callback_tool"
					source.Tools[0].InputSchema[0] = '['
					source.ToolChoice.Name = "callback_tool"
					*source.Options.MaxOutputTokens = 32
					if fail {
						return callbackFailure
					}
					return nil
				}
				var calls atomic.Int64
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, inbound *http.Request) {
					calls.Add(1)
					var body json.RawMessage
					if decodeErr := jsonv2.UnmarshalRead(inbound.Body, &body); decodeErr != nil {
						t.Error(decodeErr)
					}
					if !bytes.Contains(body, []byte("caller text")) || bytes.Contains(body, []byte("callback")) {
						t.Errorf("callback source edits changed the Core projection: %s", body)
					}
					writer.Header().Set("Content-Type", "text/event-stream")
					if protocol == "responses" {
						_, _ = fmt.Fprint(writer, "data: "+`{"type":"response.completed","response":{"id":"response","model":"model","status":"completed","output":[]}}`+"\n\n")
						return
					}
					_, _ = fmt.Fprint(writer, "data: "+`{"id":"response","model":"model","choices":[{"index":0,"delta":{"role":"assistant","content":"answer"},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
				}))
				defer server.Close()

				var model chat.Model
				if protocol == "chat" {
					model, err = openai.NewChatCompletions(t.Context(), openai.ChatCompletionsConfig{
						APIKey: "test", BaseURL: server.URL, DefaultOptions: chat.Options{Model: "model"},
					}, openai.DialectConfig{
						Provider: "test", TokenLimitField: openai.TokenLimitMaxCompletionTokens,
						PrepareRequest: func(source *chat.Request, _ *openai.CompatibleRequest) error { return mutate(source) },
					})
				} else {
					model, err = openai.NewResponses(t.Context(), openai.ResponsesConfig{
						APIKey: "test", BaseURL: server.URL, DefaultOptions: chat.Options{Model: "model"},
					}, openai.ResponsesDialect{
						Provider: "test", PrepareRequest: func(source *chat.Request, _ *openai.CompatibleResponsesRequest) error { return mutate(source) },
					})
				}
				if err != nil {
					t.Fatal(err)
				}
				_, err = model.Call(t.Context(), request)
				if fail {
					if !errors.Is(err, callbackFailure) || calls.Load() != 0 {
						t.Fatalf("Call error = %v, HTTP calls = %d", err, calls.Load())
					}
				} else if err != nil || calls.Load() != 1 {
					t.Fatalf("Call error = %v, HTTP calls = %d", err, calls.Load())
				}
				after, err := jsonv2.Marshal(request, jsonv2.Deterministic(true))
				if err != nil {
					t.Fatalf("caller request became invalid: %v", err)
				}
				if !bytes.Equal(before, after) {
					t.Fatalf("caller request changed:\nbefore: %s\nafter: %s", before, after)
				}
			})
		}
	}
}
