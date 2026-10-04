package mistral_test

import (
	"bytes"
	"encoding/binary"
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/mistral"
)

func TestChatThinkingReplayUsesCurrentParts(t *testing.T) {
	for _, closing := range []string{"", `,"closed":false`, `,"closed":true`} {
		t.Run(closing, func(t *testing.T) {
			var captured struct {
				Messages []struct {
					Content []map[string]any `json:"content"`
				} `json:"messages"`
			}
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				// The first request has string user content; capture only the replay.
				var body map[string]any
				if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
					t.Error(err)
					http.Error(writer, "invalid request", http.StatusBadRequest)
					return
				}
				if len(body["messages"].([]any)) > 1 {
					encoded, err := jsonv2.Marshal(body["messages"].([]any)[1:])
					if err != nil {
						t.Error(err)
						return
					}
					if err := jsonv2.Unmarshal(encoded, &captured.Messages); err != nil {
						t.Error(err)
						return
					}
				}
				writer.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(writer, "data: %s\n\ndata: [DONE]\n\n", `{"id":"chat-1","model":"model","choices":[{"index":0,"finish_reason":"stop","delta":{"content":[{"type":"thinking","thinking":[{"type":"text","text":"original first","native_field":7},{"type":"reference","reference_ids":[7,"doc"]},{"type":"text","text":"original second"},{"type":"tool_reference","reference_ids":["tool"]}],"signature":"native-signature","vendor_field":9`+closing+`},{"type":"text","text":"answer"}]}}]}`)
			}))
			t.Cleanup(server.Close)
			model := newThinkingModel(t, server.URL)
			user := chat.NewUserMessage(chat.NewTextPart("question"))
			response, err := model.Call(t.Context(), &chat.Request{Messages: []chat.Message{user}})
			if err != nil {
				t.Fatal(err)
			}
			message := response.Output.Message.Clone()
			if len(message.Parts) != 5 || message.Parts[0].Text != "original first" || message.Parts[2].Text != "original second" {
				t.Fatalf("native thinking children lost their boundaries: %#v", message.Parts)
			}
			message.Parts[0].Text = "edited 世界"
			message.Parts[2].Text = ""
			for _, part := range message.Parts {
				if bytes.Contains(part.ReasoningState, []byte("original")) {
					t.Fatal("replay state retains a Core text copy")
				}
			}
			encoded, err := jsonv2.Marshal(message)
			if err != nil {
				t.Fatal(err)
			}
			if err := jsonv2.Unmarshal(encoded, &message); err != nil {
				t.Fatal(err)
			}
			if _, err := model.Call(t.Context(), &chat.Request{Messages: []chat.Message{user, message}}); err != nil {
				t.Fatal(err)
			}
			content := captured.Messages[0].Content
			if len(content) != 2 || content[0]["type"] != "thinking" || content[0]["signature"] != "native-signature" || content[0]["vendor_field"] != float64(9) {
				t.Fatalf("native thinking envelope changed: %#v", content)
			}
			closed, found := content[0]["closed"]
			if closing == "" && found || closing == `,"closed":false` && closed != false || closing == `,"closed":true` && closed != true {
				t.Fatalf("native closed field changed: %#v", content[0])
			}
			children := content[0]["thinking"].([]any)
			if len(children) != 4 || children[0].(map[string]any)["text"] != "edited 世界" || children[0].(map[string]any)["native_field"] != float64(7) || children[2].(map[string]any)["text"] != "" {
				t.Fatalf("thinking text did not follow Core: %#v", children)
			}
			first := children[1].(map[string]any)
			second := children[3].(map[string]any)
			if first["type"] != "reference" || !slices.Equal(first["reference_ids"].([]any), []any{float64(7), "doc"}) || second["type"] != "tool_reference" || !slices.Equal(second["reference_ids"].([]any), []any{"tool"}) {
				t.Fatalf("thinking references changed: %#v", children)
			}
		})
	}
}

func TestChatThinkingKeepsClosedAndEmptyBlocks(t *testing.T) {
	var replay map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := jsonv2.UnmarshalRead(request.Body, &replay); err != nil {
			t.Error(err)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(writer, "data: %s\n\ndata: [DONE]\n\n", `{"id":"chat-1","model":"model","choices":[{"index":0,"finish_reason":"stop","delta":{"content":[{"type":"thinking","thinking":[{"type":"text","text":"same"}],"closed":true},{"type":"thinking","thinking":[],"closed":true,"signature":null},{"type":"thinking","thinking":[{"type":"text","text":"same"}],"closed":true},{"type":"text","text":"answer"}]}}]}`)
	}))
	t.Cleanup(server.Close)
	model := newThinkingModel(t, server.URL)
	user := chat.NewUserMessage(chat.NewTextPart("question"))
	response, err := model.Call(t.Context(), &chat.Request{Messages: []chat.Message{user}})
	if err != nil {
		t.Fatal(err)
	}
	parts := response.Output.Message.Parts
	if len(parts) != 4 || parts[0].Text != "same" || parts[1].Text != "" || parts[2].Text != "same" {
		t.Fatalf("closed thinking blocks merged: %#v", parts)
	}
	if _, err := model.Call(t.Context(), &chat.Request{Messages: []chat.Message{user, response.Output.Message.Clone()}}); err != nil {
		t.Fatal(err)
	}
	content := replay["messages"].([]any)[1].(map[string]any)["content"].([]any)
	if len(content) != 4 {
		t.Fatalf("closed block boundaries changed: %#v", content)
	}
	empty := content[1].(map[string]any)
	_, signaturePresent := empty["signature"]
	if empty["type"] != "thinking" || len(empty["thinking"].([]any)) != 0 || empty["closed"] != true || !signaturePresent || empty["signature"] != nil {
		t.Fatalf("empty closing block changed: %#v", empty)
	}
}

func TestChatRejectsInvalidThinkingStateBeforeIO(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(writer, "unexpected provider call", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	model := newThinkingModel(t, server.URL)
	for _, state := range []string{
		`{"type":"thinking","thinking":[{"type":"text","text":"old copy"}],"closed":true}`,
		`{"block":1,"content":{"type":"text","text":"second owner"}}`,
		`{"block":1,"fields":{"thinking":[]}}`,
		`{"block":0}`,
		`{"block":1,"fields":{"closed":null}}`,
		`{"block":1,"unknown":true}`,
		`null`,
	} {
		frame := make([]byte, 8+len(state))
		copy(frame, "MSTH")
		binary.BigEndian.PutUint32(frame[4:8], uint32(len(state)))
		copy(frame[8:], state)
		request := &chat.Request{Messages: []chat.Message{
			chat.NewUserMessage(chat.NewTextPart("question")),
			chat.NewAssistantMessage(chat.NewReasoningPart("current", frame)),
		}}
		if _, err := model.Call(t.Context(), request); err == nil {
			t.Errorf("replayed invalid thinking state %s", state)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("invalid state reached provider %d time(s)", hits.Load())
	}
}

func TestChatRejectsMalformedNativeThinkingText(t *testing.T) {
	for _, child := range []string{`{"type":"text","text":7}`, `{"type":"text","text":null}`, `{"type":"text"}`} {
		t.Run(child, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(writer, "data: %s\n\ndata: [DONE]\n\n", `{"id":"chat-1","model":"model","choices":[{"index":0,"finish_reason":"stop","delta":{"content":[{"type":"thinking","thinking":[`+child+`],"closed":true}]}}]}`)
			}))
			t.Cleanup(server.Close)
			model := newThinkingModel(t, server.URL)
			request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("question"))}}
			if _, err := model.Call(t.Context(), request); err == nil {
				t.Fatal("malformed native text became a successful response")
			}
		})
	}
}

func newThinkingModel(t *testing.T, baseURL string) *mistral.Chat {
	t.Helper()
	model, err := mistral.NewChat(t.Context(), mistral.ChatConfig{
		APIKey: "test-key", BaseURL: baseURL, DefaultOptions: chat.Options{Model: "model"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return model
}
