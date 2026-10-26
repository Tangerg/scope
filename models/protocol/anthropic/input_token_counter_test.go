package anthropic_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/models/protocol/anthropic"
)

func TestChatCountsTheSameMultimodalMessageInput(t *testing.T) {
	type countRequest struct {
		Model     string            `json:"model"`
		Messages  []json.RawMessage `json:"messages"`
		System    []json.RawMessage `json:"system"`
		Tools     []json.RawMessage `json:"tools"`
		MaxTokens *int64            `json:"max_tokens"`
	}
	var captured countRequest
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !strings.HasSuffix(request.URL.Path, "/messages/count_tokens") {
			t.Errorf("path = %q, want /messages/count_tokens", request.URL.Path)
		}
		if err := jsonv2.UnmarshalRead(request.Body, &captured); err != nil {
			t.Errorf("decode count request: %v", err)
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"input_tokens":287}`))
	}))
	t.Cleanup(server.Close)

	image, err := media.NewBytes("image/png", []byte("provider-counted-image"))
	if err != nil {
		t.Fatal(err)
	}
	model, err := anthropic.NewMessages(t.Context(), anthropic.MessagesConfig{
		APIKey:         "test-key",
		DefaultOptions: chat.Options{Model: "claude-test"},
		BaseURL:        server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := &chat.Request{
		Messages: []chat.Message{
			chat.NewSystemMessage("frozen instructions"),
			chat.NewUserMessage(chat.NewTextPart("inspect"), chat.NewMediaPart(image)),
		},
		Tools: []chat.ToolDefinition{{
			Name: "inspect_image", Description: "Inspect an image",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		}},
	}
	count, err := model.CountInputTokens(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if count != 287 {
		t.Fatalf("CountInputTokens = %d, want 287", count)
	}
	if captured.Model != "claude-test" || len(captured.Messages) != 1 || len(captured.System) != 1 || len(captured.Tools) != 1 {
		t.Fatalf("count request = %#v", captured)
	}
	if captured.MaxTokens != nil {
		t.Fatalf("count request leaked generation-only max_tokens: %d", *captured.MaxTokens)
	}
	if !strings.Contains(string(captured.Messages[0]), "base64") {
		t.Fatalf("count request omitted the inline image: %s", captured.Messages[0])
	}
}

func TestRemoteCountsRequirePresentNonnegativeInputTokens(t *testing.T) {
	for _, test := range []struct {
		body       string
		wantTokens *int64
	}{
		{body: `{}`},
		{body: `null`},
		{body: `{"input_tokens":null}`},
		{body: `{"input_tokens":"bogus"}`},
		{body: `{"input_tokens":-1}`},
		{body: `{"input_tokens":0}`, wantTokens: new(int64(0))},
		{body: `{"input_tokens":7}`, wantTokens: new(int64(7))},
	} {
		t.Run(test.body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			model, err := anthropic.NewMessages(t.Context(), anthropic.MessagesConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: chat.Options{Model: "model"}})
			if err != nil {
				t.Fatal(err)
			}
			counter, err := anthropic.NewTextCounter(t.Context(), anthropic.TextCounterConfig{APIKey: "test", BaseURL: server.URL, Model: "model"})
			if err != nil {
				t.Fatal(err)
			}
			request, _ := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("text")))
			count, modelErr := model.CountInputTokens(t.Context(), request)
			textCount, textErr := counter.CountText(t.Context(), "text")
			if test.wantTokens == nil {
				if modelErr == nil || textErr == nil {
					t.Fatalf("invalid count accepted: count=%d/%d errors=%v/%v", count, textCount, modelErr, textErr)
				}
				return
			}
			if modelErr != nil || textErr != nil || count != *test.wantTokens || int64(textCount) != *test.wantTokens {
				t.Fatalf("count=%d/%d want=%d errors=%v/%v", count, textCount, *test.wantTokens, modelErr, textErr)
			}
		})
	}
}
