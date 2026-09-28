package openai_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/models/protocol/openai"
)

func TestChatAudioRetainsPayloadAndReplayIdentity(t *testing.T) {
	for _, test := range []struct {
		name  string
		audio string
		id    string
		data  string
	}{
		{"payload and identity", `{"id":"audio-1","data":"YXVkaW8="}`, "audio-1", "audio"},
		{"payload only", `{"data":"YXVkaW8="}`, "", "audio"},
		{"identity only", `{"id":"audio-1"}`, "audio-1", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			var captured []struct {
				Messages []struct {
					Audio struct {
						ID string `json:"id"`
					} `json:"audio"`
				} `json:"messages"`
			}
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				var body struct {
					Messages []struct {
						Audio struct {
							ID string `json:"id"`
						} `json:"audio"`
					} `json:"messages"`
				}
				if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
					t.Error(err)
				}
				captured = append(captured, body)
				writer.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(writer, "data: %s\n\n", `{"id":"chat-1","model":"audio-model","choices":[{"index":0,"delta":{"audio":`+test.audio+`},"finish_reason":"stop"}]}`)
				fmt.Fprint(writer, "data: [DONE]\n\n")
			}))
			defer server.Close()
			model, err := openai.NewChatCompletions(t.Context(), openai.ChatCompletionsConfig{
				APIKey: "test", BaseURL: server.URL, DefaultOptions: chat.Options{Model: "audio-model"},
			})
			if err != nil {
				t.Fatal(err)
			}
			user := chat.NewUserMessage(chat.NewTextPart("speak"))
			request := &chat.Request{Messages: []chat.Message{user}}
			if setErr := request.Options.Extensions.Set(openai.RequestExtensionKey, map[string]any{"audio": map[string]any{"format": "mp3", "voice": "alloy"}, "modalities": []string{"audio"}}); setErr != nil {
				t.Fatal(setErr)
			}
			response, err := model.Call(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			parts := response.Output.Message.Parts
			if len(parts) != 1 || parts[0].Media == nil {
				t.Fatalf("parts = %#v", parts)
			}
			value := parts[0].Media
			if value.ID != test.id || value.MIME != "audio/mpeg" {
				t.Fatalf("media = %#v", value)
			}
			if test.data != "" {
				data, bytesErr := value.Bytes()
				if bytesErr != nil || string(data) != test.data {
					t.Fatalf("bytes = %q, %v", data, bytesErr)
				}
			} else if reference, referenceErr := value.Reference(); referenceErr != nil || reference != test.id {
				t.Fatalf("reference = %q, %v", reference, referenceErr)
			}
			request.Messages = append(request.Messages, response.Output.Message.Clone(), chat.NewUserMessage(chat.NewTextPart("again")))
			_, err = model.Call(t.Context(), request)
			if test.id == "" {
				if err == nil || !strings.Contains(err.Error(), "provider reference") || len(captured) != 1 {
					t.Fatalf("unidentified replay = %v, calls=%d", err, len(captured))
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if len(captured) != 2 || captured[1].Messages[1].Audio.ID != test.id {
					t.Fatalf("replay = %#v", captured)
				}
			}
		})
	}
}

func TestChatMediaInputWireContract(t *testing.T) {
	for _, test := range []struct {
		name    string
		mime    string
		kind    media.SourceKind
		source  string
		want    string
		wantErr string
	}{
		{"MP3 MIME", "audio/mpeg", media.SourceBytes, "audio", `{"type":"input_audio","input_audio":{"data":"YXVkaW8=","format":"mp3"}}`, ""},
		{"WAV MIME", "audio/wav", media.SourceBytes, "audio", `{"type":"input_audio","input_audio":{"data":"YXVkaW8=","format":"wav"}}`, ""},
		{"file bytes", "application/pdf", media.SourceBytes, "pdf", `{"type":"file","file":{"file_data":"data:application/pdf;base64,cGRm","filename":"file.pdf"}}`, ""},
		{"file reference", "application/pdf", media.SourceReference, "file-1", `{"type":"file","file":{"file_id":"file-1","filename":"file.pdf"}}`, ""},
		{"file URI", "application/pdf", media.SourceURI, "https://example.com/report.pdf", "", "does not support file URIs"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var captured json.RawMessage
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				var body struct {
					Messages []struct {
						Content []json.RawMessage `json:"content"`
					} `json:"messages"`
				}
				if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
					t.Error(err)
				}
				captured = body.Messages[0].Content[0]
				writer.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(writer, "data: {\"id\":\"chat-1\",\"model\":\"test-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()
			var value *media.Media
			var err error
			switch test.kind {
			case media.SourceBytes:
				value, err = media.NewBytes(test.mime, []byte(test.source))
			case media.SourceReference:
				value, err = media.NewReference(test.mime, test.source)
			case media.SourceURI:
				value, err = media.NewURI(test.mime, test.source)
			}
			if err != nil {
				t.Fatal(err)
			}
			value.Name = "file.pdf"
			model, err := openai.NewChatCompletions(t.Context(), openai.ChatCompletionsConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: chat.Options{Model: "test-model"}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = model.Call(t.Context(), &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewMediaPart(value))}})
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) || len(captured) != 0 {
					t.Fatalf("Call = %v, wire = %s", err, captured)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var actual, expected any
			if err := jsonv2.Unmarshal(captured, &actual); err != nil {
				t.Fatal(err)
			}
			if err := jsonv2.Unmarshal([]byte(test.want), &expected); err != nil {
				t.Fatal(err)
			}
			actualJSON, _ := jsonv2.Marshal(actual, jsonv2.Deterministic(true))
			expectedJSON, _ := jsonv2.Marshal(expected, jsonv2.Deterministic(true))
			if string(actualJSON) != string(expectedJSON) {
				t.Fatalf("wire = %s, want %s", actualJSON, expectedJSON)
			}
		})
	}
}
