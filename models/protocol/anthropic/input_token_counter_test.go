package anthropic_test

import (
	"encoding/json"
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
		if err := json.NewDecoder(request.Body).Decode(&captured); err != nil {
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
