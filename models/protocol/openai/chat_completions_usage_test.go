package openai_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	corechat "github.com/Tangerg/scope/core/chat"
	scopeopenai "github.com/Tangerg/scope/models/protocol/openai"
)

func TestChatUsageRequestPolicy(t *testing.T) {
	for _, test := range []struct {
		name          string
		compatible    bool
		streamOptions any
		supplied      bool
		include       bool
		omitUsage     bool
	}{
		{name: "standard default", include: true},
		{name: "explicit true", supplied: true, streamOptions: map[string]any{"include_usage": true}, include: true},
		{name: "explicit null", supplied: true},
		{name: "explicit false", supplied: true, streamOptions: map[string]any{"include_usage": false}},
		{name: "other stream options", supplied: true, streamOptions: map[string]any{"include_obfuscation": false}, include: true},
		{name: "compatible default", compatible: true},
		{name: "compatible explicit", compatible: true, supplied: true, streamOptions: map[string]any{"include_usage": true}, include: true},
		{name: "usage unavailable", include: true, omitUsage: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var wire struct {
					Stream        bool           `json:"stream"`
					StreamOptions map[string]any `json:"stream_options"`
				}
				if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
					t.Error(err)
					return
				}
				if !wire.Stream {
					t.Error("request is not streaming")
				}
				include, _ := wire.StreamOptions["include_usage"].(bool)
				if include != test.include {
					t.Errorf("include_usage = %v; want %v", wire.StreamOptions, test.include)
				}
				if test.compatible && !test.supplied && wire.StreamOptions != nil {
					t.Errorf("injected options into compatible endpoint: %v", wire.StreamOptions)
				}
				if test.name == "other stream options" && wire.StreamOptions["include_obfuscation"] != false {
					t.Error("lost explicit stream option")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"id\":\"r\",\"model\":\"gpt-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":\"stop\"}]}\n\n")
				if include && !test.omitUsage {
					fmt.Fprint(w, "data: {\"id\":\"r\",\"model\":\"gpt-5.2\",\"choices\":[],\"usage\":{\"prompt_tokens\":8,\"completion_tokens\":4,\"total_tokens\":12}}\n\n")
				}
				fmt.Fprint(w, "data: [DONE]\n\n")
			}))
			defer server.Close()
			config := scopeopenai.ChatCompletionsConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: corechat.Options{Model: "gpt-5.2"}}
			key := scopeopenai.RequestExtensionKey
			var model *scopeopenai.ChatCompletions
			var err error
			if test.compatible {
				key = "custom/openai_request"
				model, err = scopeopenai.NewCompatibleChatCompletions(t.Context(), config, scopeopenai.Dialect{Provider: "custom", TokenLimitField: scopeopenai.TokenLimitMaxTokens})
			} else {
				model, err = scopeopenai.NewChatCompletions(t.Context(), config)
			}
			if err != nil {
				t.Fatal(err)
			}
			req := newCoreChatRequest(t)
			if test.supplied {
				if setErr := req.Options.Extensions.Set(key, scopeopenai.RequestFields{"stream_options": test.streamOptions}); setErr != nil {
					t.Fatal(setErr)
				}
			}
			response, err := model.Call(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			usage := response.Metadata.Usage
			if test.include && !test.omitUsage {
				if usage == nil || usage.InputTokens != 8 || usage.OutputTokens != 4 || usage.TotalTokens() != 12 {
					t.Fatalf("usage = %+v", usage)
				}
			} else if usage != nil {
				t.Fatalf("invented usage = %+v", usage)
			}
		})
	}
}

func TestChatCancellationWhileAwaitingUsage(t *testing.T) {
	for _, operation := range []string{"call", "stream"} {
		t.Run(operation, func(t *testing.T) {
			ready := make(chan struct{})
			closed := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"id\":\"r\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":\"stop\"}]}\n\n")
				w.(http.Flusher).Flush()
				close(ready)
				<-r.Context().Done()
				close(closed)
			}))
			defer server.Close()
			model := newOpenAIBehaviorChat(t, server.URL)
			req := newCoreChatRequest(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				if operation == "call" {
					response, err := model.Call(ctx, req)
					if response != nil {
						err = fmt.Errorf("canceled call returned a response")
					}
					done <- err
					return
				}
				var failure error
				for _, err := range model.Stream(ctx, req) {
					if err != nil {
						failure = err
					}
				}
				done <- failure
			}()
			select {
			case <-ready:
			case <-time.After(5 * time.Second):
				t.Fatal("request not started")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel error=%v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("canceled request did not terminate")
			}
			select {
			case <-closed:
			case <-time.After(5 * time.Second):
				t.Fatal("transport not closed")
			}
		})
	}
}
