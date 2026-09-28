package deepseek_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/deepseek"
)

func TestThinkingUsesEffectiveCoreOptions(t *testing.T) {
	for _, test := range []struct {
		name        string
		effort      chat.ReasoningEffort
		topP        *float64
		temperature *float64
		choice      *chat.ToolChoice
		wantErr     string
	}{
		{name: "none permits temperature", effort: "none", temperature: new(0.7)},
		{name: "thinking accepts top_p", effort: "high", topP: new(0.99)},
		{name: "default thinking accepts minimum top_p", topP: new(0.95)},
		{name: "thinking refuses clamped top_p", effort: "high", topP: new(0.94), wantErr: "options.top_p must be at least"},
		{name: "non-thinking refuses ignored top_p", effort: "none", topP: new(0.99), wantErr: "options.top_p has no effect"},
		{name: "thinking refuses ignored temperature", effort: "max", temperature: new(0.7), wantErr: "options.temperature has no effect"},
		{name: "thinking refuses forced tools", effort: "high", choice: &chat.ToolChoice{Mode: chat.ToolChoiceNamed, Name: "lookup"}, wantErr: "not supported while DeepSeek thinking"},
		{name: "non-thinking permits forced tools", effort: "none", choice: &chat.ToolChoice{Mode: chat.ToolChoiceNamed, Name: "lookup"}},
		{name: "minimal alias", effort: "minimal"}, {name: "medium alias", effort: "medium"}, {name: "xhigh alias", effort: "xhigh"},
	} {
		for _, defaults := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/defaults=%v", test.name, defaults), func(t *testing.T) {
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					calls++
					var body map[string]any
					if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
						t.Error(err)
					}
					if test.effort != "" && body["reasoning_effort"] != string(test.effort) {
						t.Errorf("reasoning_effort = %v", body["reasoning_effort"])
					}
					if test.topP != nil && body["top_p"] != *test.topP {
						t.Errorf("top_p = %v", body["top_p"])
					}
					if test.temperature != nil && body["temperature"] != *test.temperature {
						t.Errorf("temperature = %v", body["temperature"])
					}
					if _, exists := body["thinking"]; exists {
						t.Errorf("duplicate thinking toggle = %v", body["thinking"])
					}
					writer.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(writer, "data: {\"id\":\"chat-1\",\"model\":\"deepseek-flash\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				}))
				defer server.Close()
				options := chat.Options{Model: deepseek.ModelFlash, ReasoningEffort: test.effort, TopP: test.topP, Temperature: test.temperature}
				config := deepseek.ChatConfig{APIKey: "test", BaseURL: server.URL}
				request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}, ToolChoice: test.choice, Tools: []chat.ToolDefinition{{Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)}}}
				if defaults {
					config.DefaultOptions = options
				} else {
					request.Options = options
				}
				model, err := deepseek.NewChat(t.Context(), config)
				if err == nil {
					_, err = model.Call(t.Context(), request)
				}
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

func TestChatRejectsObsoleteThinkingExtension(t *testing.T) {
	request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}}
	if err := request.Options.Extensions.Set(deepseek.RequestExtensionKey, map[string]any{"thinking": map[string]any{"type": "disabled"}}); err != nil {
		t.Fatal(err)
	}
	model, err := deepseek.NewChat(t.Context(), deepseek.ChatConfig{APIKey: "test", BaseURL: "http://127.0.0.1:1", DefaultOptions: chat.Options{Model: deepseek.ModelFlash}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := model.Call(t.Context(), request); err == nil || !strings.Contains(err.Error(), "owned by options.reasoning_effort") {
		t.Fatalf("Call = %v", err)
	}
}
