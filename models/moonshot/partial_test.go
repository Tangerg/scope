package moonshot_test

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/moonshot"
)

func TestPartialBelongsToFinalAssistantMessage(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprint(partial), func(t *testing.T) {
			var body struct {
				Partial  *bool `json:"partial"`
				Messages []struct {
					Role    string `json:"role"`
					Content string `json:"content"`
					Partial *bool  `json:"partial"`
				} `json:"messages"`
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := jsonv2.UnmarshalRead(r.Body, &body); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, `data: {"id":"chat-1","model":"kimi-k3","choices":[{"index":0,"finish_reason":"stop","delta":{"role":"assistant","content":"answer"}}],"object":"chat.completion.chunk"}`+"\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()
			model, err := moonshot.NewChatCompletions(t.Context(), moonshot.ChatCompletionsConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: corechat.Options{Model: moonshot.ModelK3}})
			if err != nil {
				t.Fatal(err)
			}
			request, err := corechat.NewRequest(corechat.NewUserMessage(corechat.NewTextPart("continue JSON")), corechat.NewAssistantMessage(corechat.NewTextPart("{")))
			if err != nil {
				t.Fatal(err)
			}
			if err := request.Options.Extensions.Set(moonshot.RequestExtensionKey, moonshot.ChatRequestOptions{Partial: new(partial)}); err != nil {
				t.Fatal(err)
			}
			if _, err := model.Call(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			if body.Partial != nil || len(body.Messages) != 2 || body.Messages[0].Partial != nil {
				t.Fatalf("wrong partial ownership: %+v", body)
			}
			last := body.Messages[1]
			if last.Role != "assistant" || last.Content != "{" || last.Partial == nil || *last.Partial != partial {
				t.Fatalf("final assistant message = %+v", last)
			}
		})
	}
}

func TestPartialRejectsNonTextPrefixBeforeDispatch(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		http.Error(w, "unexpected call", http.StatusBadRequest)
	}))
	defer server.Close()
	model, err := moonshot.NewChatCompletions(t.Context(), moonshot.ChatCompletionsConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: corechat.Options{Model: moonshot.ModelK3}})
	if err != nil {
		t.Fatal(err)
	}
	for _, last := range []corechat.Message{
		corechat.NewUserMessage(corechat.NewTextPart("continue")),
		corechat.NewAssistantMessage(corechat.NewReasoningPart("thinking", nil)),
	} {
		request := &corechat.Request{Messages: []corechat.Message{last}}
		if err := request.Options.Extensions.Set(moonshot.RequestExtensionKey, moonshot.ChatRequestOptions{Partial: new(true)}); err != nil {
			t.Fatal(err)
		}
		if _, err := model.Call(t.Context(), request); err == nil {
			t.Fatal("non-text prefix admitted")
		}
	}
	if calls != 0 {
		t.Fatalf("requests = %d", calls)
	}
}

func TestPartialRejectsCompetingJSONMode(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		http.Error(w, "unexpected call", http.StatusBadRequest)
	}))
	defer server.Close()
	model, err := moonshot.NewChatCompletions(t.Context(), moonshot.ChatCompletionsConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: corechat.Options{Model: moonshot.ModelK3}})
	if err != nil {
		t.Fatal(err)
	}
	request, err := corechat.NewRequest(corechat.NewUserMessage(corechat.NewTextPart("continue JSON")), corechat.NewAssistantMessage(corechat.NewTextPart("{")))
	if err != nil {
		t.Fatal(err)
	}
	request.Options.OutputFormat = &corechat.OutputFormat{Type: corechat.OutputFormatJSON}
	if err := request.Options.Extensions.Set(moonshot.RequestExtensionKey, moonshot.ChatRequestOptions{Partial: new(true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := model.Call(t.Context(), request); err == nil || calls != 0 {
		t.Fatalf("error=%v calls=%d", err, calls)
	}
}
