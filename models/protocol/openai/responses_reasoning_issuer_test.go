package openai_test

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/protocol/openai"
)

func TestResponsesReasoningReplaysOnlyToItsIssuer(t *testing.T) {
	for _, issuer := range []string{openai.ProviderID, "perplexity"} {
		t.Run(issuer, func(t *testing.T) {
			item := `{"id":"reasoning-origin","type":"reasoning","summary":[{"type":"summary_text","text":"original"}],"encrypted_content":"issuer-ciphertext","status":"completed","vendor_integer":9007199254740993}`
			origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(writer, "data: "+`{"type":"response.reasoning_summary_text.delta","output_index":0,"item_id":"reasoning-origin","summary_index":0,"delta":"original"}`+"\n\n")
				_, _ = fmt.Fprint(writer, "data: "+`{"type":"response.output_item.done","output_index":0,"item":`+item+`}`+"\n\n")
				_, _ = fmt.Fprint(writer, "data: "+`{"type":"response.completed","response":{"id":"origin","model":"model","status":"completed","output":[`+item+`]}}`+"\n\n")
			}))
			defer origin.Close()
			source, err := openai.NewResponses(t.Context(), openai.ResponsesConfig{
				APIKey: "source-key", BaseURL: origin.URL, DefaultOptions: chat.Options{Model: "model"},
			}, openai.ResponsesDialect{Provider: issuer})
			if err != nil {
				t.Fatal(err)
			}
			question := chat.NewUserMessage(chat.NewTextPart("question"))
			response, err := source.Call(t.Context(), &chat.Request{Messages: []chat.Message{question}})
			if err != nil {
				t.Fatal(err)
			}
			history := response.Output.Message.Clone()
			history.Parts[0].Text = "edited summary"
			encoded, err := jsonv2.Marshal(history)
			if err != nil {
				t.Fatal(err)
			}
			if err := jsonv2.Unmarshal(encoded, &history); err != nil {
				t.Fatal(err)
			}
			for _, provider := range []string{openai.ProviderID, "perplexity"} {
				t.Run(provider, func(t *testing.T) {
					captured := make(chan json.RawMessage, 1)
					destination := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
						var body struct {
							Input json.RawMessage `json:"input"`
						}
						if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
							t.Error(err)
						}
						captured <- body.Input
						if strings.HasSuffix(request.URL.Path, "/input_tokens") {
							writer.Header().Set("Content-Type", "application/json")
							_, _ = fmt.Fprint(writer, `{"input_tokens":11}`)
							return
						}
						writer.Header().Set("Content-Type", "text/event-stream")
						_, _ = fmt.Fprint(writer, "data: "+`{"type":"response.completed","response":{"id":"destination","model":"model","status":"completed","output":[]}}`+"\n\n")
					}))
					defer destination.Close()
					target, err := openai.NewResponses(t.Context(), openai.ResponsesConfig{
						APIKey: "target-key", BaseURL: destination.URL, DefaultOptions: chat.Options{Model: "model"},
					}, openai.ResponsesDialect{Provider: provider})
					if err != nil {
						t.Fatal(err)
					}
					request := &chat.Request{Messages: []chat.Message{question, history, chat.NewUserMessage(chat.NewTextPart("continue"))}}
					if _, err := target.Call(t.Context(), request); err != nil {
						t.Fatal(err)
					}
					assertResponsesIssuerInput(t, <-captured, provider == issuer)
					if provider == openai.ProviderID {
						if count, err := target.CountInputTokens(t.Context(), request); err != nil || count != 11 {
							t.Fatalf("CountInputTokens = %d, %v", count, err)
						}
						assertResponsesIssuerInput(t, <-captured, provider == issuer)
					}
				})
			}
		})
	}
}

func assertResponsesIssuerInput(t *testing.T, input json.RawMessage, ownIssuer bool) {
	t.Helper()
	for _, marker := range []string{"reasoning-origin", "issuer-ciphertext", "edited summary", "9007199254740993"} {
		if found := bytes.Contains(input, []byte(marker)); found != ownIssuer {
			t.Errorf("input contains %q = %t, own issuer = %t: %s", marker, found, ownIssuer, input)
		}
	}
	if bytes.Contains(input, []byte("original")) {
		t.Errorf("reasoning text has a stale owner: %s", input)
	}
}
