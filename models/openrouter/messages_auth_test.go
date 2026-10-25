package openrouter_test

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/openrouter"
)

func TestMessagesUsesOpenRouterBearerAuthentication(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: messagesAuthTransport(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.URL.String() != "https://openrouter.ai/api/v1/messages" {
			t.Errorf("endpoint = %q", request.URL)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q; want Bearer test-key", got)
		}
		if got := request.Header.Get("X-Api-Key"); got != "" {
			t.Errorf("unexpected Anthropic X-Api-Key = %q", got)
		}
		if request.Header.Get(openrouter.HeaderReferer) != "https://app.test" || request.Header.Get(openrouter.HeaderAppTitle) != "test app" {
			t.Error("app attribution headers were not preserved")
		}
		var body strings.Builder
		for _, event := range []struct {
			kind string
			data string
		}{
			{kind: "message_start", data: `{"type":"message_start","message":{"id":"msg-1","type":"message","role":"assistant","model":"claude-test","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`},
			{kind: "content_block_start", data: `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"answer"}}`},
			{kind: "content_block_stop", data: `{"type":"content_block_stop","index":0}`},
			{kind: "message_delta", data: `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`},
			{kind: "message_stop", data: `{"type":"message_stop"}`},
		} {
			fmt.Fprintf(&body, "event: %s\ndata: %s\n\n", event.kind, event.data)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body.String())), Request: request}, nil
	})}
	model, err := openrouter.NewMessages(t.Context(), openrouter.MessagesConfig{APIKey: "test-key", HTTPClient: client, AppURL: "https://app.test", AppTitle: "test app", DefaultOptions: chat.Options{Model: "claude-test", MaxOutputTokens: new(int64(64))}})
	if err != nil {
		t.Fatal(err)
	}
	request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}}
	response, err := model.Call(t.Context(), request)
	if err != nil || response.Text() != "answer" || calls != 1 {
		t.Fatalf("Call = %#v, %v; requests = %d", response, err, calls)
	}
}

type messagesAuthTransport func(*http.Request) (*http.Response, error)

func (m messagesAuthTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return m(request)
}
