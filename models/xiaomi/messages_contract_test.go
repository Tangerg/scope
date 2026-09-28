package xiaomi_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/xiaomi"
)

func TestMiMoProtocolsShareRequestPolicy(t *testing.T) {
	for _, protocol := range []string{"chat", "messages"} {
		for _, test := range []struct {
			name           string
			thinking       xiaomi.ThinkingType
			temperature    *float64
			topP           *float64
			choice         *chat.ToolChoice
			nativeThinking bool
			wantErr        string
		}{
			{name: "default thinking"},
			{name: "ignored temperature", temperature: new(0.7), wantErr: "options.temperature"},
			{name: "ignored top_p", topP: new(0.9), wantErr: "options.top_p"},
			{name: "named tool", choice: &chat.ToolChoice{Mode: chat.ToolChoiceNamed, Name: "lookup"}, wantErr: "tool_choice"},
			{name: "required tool", choice: &chat.ToolChoice{Mode: chat.ToolChoiceRequired}, wantErr: "tool_choice"},
			{name: "forbidden tools", choice: &chat.ToolChoice{Mode: chat.ToolChoiceNone}, wantErr: "tool_choice"},
			{name: "disabled thinking sampling", thinking: xiaomi.ThinkingDisabled, temperature: new(0.7), topP: new(0.9)},
			{name: "minimum top_p", thinking: xiaomi.ThinkingDisabled, topP: new(0.01)},
			{name: "top_p below minimum", thinking: xiaomi.ThinkingDisabled, topP: new(0.005), wantErr: "top_p must"},
			{name: "out of range temperature", thinking: xiaomi.ThinkingDisabled, temperature: new(1.6), wantErr: "temperature must"},
			{name: "duplicate thinking source", nativeThinking: true, wantErr: "thinking is owned"},
		} {
			t.Run(protocol+"/"+test.name, func(t *testing.T) {
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					calls++
					var body map[string]any
					if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
						t.Error(err)
					}
					if test.temperature != nil && body["temperature"] != *test.temperature {
						t.Errorf("temperature = %v", body["temperature"])
					}
					if test.topP != nil && body["top_p"] != *test.topP {
						t.Errorf("top_p = %v", body["top_p"])
					}
					if test.thinking != "" {
						thinking, ok := body["thinking"].(map[string]any)
						if !ok || thinking["type"] != string(test.thinking) {
							t.Errorf("thinking = %#v", body["thinking"])
						}
					}
					writer.Header().Set("Content-Type", "text/event-stream")
					if protocol == "chat" {
						if request.Header.Get("Authorization") != "Bearer test-key" {
							t.Errorf("chat authentication = %v", request.Header)
						}
						fmt.Fprint(writer, "data: {\"id\":\"chat-1\",\"model\":\"mimo-v2.6-pro\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
						return
					}
					if request.Header.Get("api-key") != "test-key" {
						t.Errorf("Messages api-key = %q", request.Header.Get("api-key"))
					}
					if _, exists := request.Header["X-Api-Key"]; exists {
						t.Errorf("undocumented x-api-key sent: %v", request.Header)
					}
					fmt.Fprint(writer, `event: message_start
data: {"type":"message_start","message":{"id":"msg-1","type":"message","role":"assistant","model":"mimo-v2.6-pro","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":1}}

event: message_stop
data: {"type":"message_stop"}

`)
				}))
				defer server.Close()
				options := chat.Options{Model: "mimo-v2.6-pro", Temperature: test.temperature, TopP: test.topP}
				if test.thinking != "" {
					if err := options.Extensions.Set(xiaomi.RequestExtensionKey, xiaomi.ChatRequestOptions{Thinking: test.thinking}); err != nil {
						t.Fatal(err)
					}
				}
				if test.nativeThinking {
					key := xiaomi.OpenAIRequestExtensionKey
					if protocol == "messages" {
						key = xiaomi.AnthropicRequestExtensionKey
					}
					if err := options.Extensions.Set(key, map[string]any{"thinking": map[string]any{"type": "disabled"}}); err != nil {
						t.Fatal(err)
					}
				}
				var model chat.Model
				var err error
				if protocol == "chat" {
					model, err = xiaomi.NewChat(t.Context(), xiaomi.ChatConfig{APIKey: "test-key", BaseURL: server.URL, DefaultOptions: options})
				} else {
					model, err = xiaomi.NewMessages(t.Context(), xiaomi.MessagesConfig{APIKey: "test-key", BaseURL: server.URL, DefaultOptions: options})
				}
				if err != nil {
					t.Fatal(err)
				}
				_, err = model.Call(t.Context(), &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}, ToolChoice: test.choice, Tools: []chat.ToolDefinition{{Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)}}})
				if test.wantErr != "" {
					if err == nil || !strings.Contains(err.Error(), test.wantErr) || calls != 0 {
						t.Fatalf("err=%v calls=%d", err, calls)
					}
				} else if err != nil || calls != 1 {
					t.Fatalf("err=%v calls=%d", err, calls)
				}
			})
		}
	}
}
