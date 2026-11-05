package google_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/genai"

	"github.com/Tangerg/scope/core/chat"
)

func TestGenerationStreamUsesSSEFraming(t *testing.T) {
	const data = `{"candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"answer"}]},"finishReason":"STOP"}]}`
	for _, provider := range []string{"google", "vertexai"} {
		for _, sample := range []struct {
			name, prefix, line, blank string
		}{
			{"LF", "", "\n", "\n"},
			{"CRLF", "", "\r\n", "\r\n"},
			{"CR", "", "\r", "\r"},
			{"mixed", "", "\r\n", "\r"},
			{"BOM", "\xef\xbb\xbf", "\n", "\n"},
		} {
			t.Run(provider+"/"+sample.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, sample.prefix+"data: "+data+sample.line+sample.blank)
				}))
				t.Cleanup(server.Close)
				request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}}
				response, err := newGenerationChat(t, provider, server).Call(t.Context(), request)
				if err != nil || response == nil || response.Text() != "answer" || response.Output.FinishReason != chat.FinishReasonStop {
					t.Fatalf("Call = %#v, %v; want completed answer", response, err)
				}
			})
		}
	}
}

func TestGenerationStreamCannotCompleteAnUnterminatedSSEEvent(t *testing.T) {
	const start = "data: {\"candidates\":[{\"index\":0,\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"partial\"}]}}]}\n\n"
	const stop = `data: {"candidates":[{"index":0,"finishReason":"STOP"}]}`
	for _, provider := range []string{"google", "vertexai"} {
		for _, suffix := range []string{"", "\n", "\r\n", "\r"} {
			t.Run(fmt.Sprintf("%s/%q", provider, suffix), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, start+stop+suffix)
				}))
				t.Cleanup(server.Close)
				request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}}
				model := newGenerationChat(t, provider, server)
				if response, err := model.Call(t.Context(), request); response != nil || !errors.Is(err, chat.ErrInvalidResponse) {
					t.Fatalf("Call = %#v, %v; want incomplete stream error", response, err)
				}
				var failure error
				for delta, err := range model.(chat.Streamer).Stream(t.Context(), request) {
					if err != nil {
						failure = err
						break
					}
					if delta.FinishReason != "" {
						t.Fatalf("unterminated event completed generation: %#v", delta)
					}
				}
				if !errors.Is(failure, chat.ErrInvalidResponse) {
					t.Fatalf("Stream error = %v; want incomplete stream error", failure)
				}
			})
		}
	}
}

func TestGenerationStreamPreservesSSEDataAndErrorEvents(t *testing.T) {
	for _, provider := range []string{"google", "vertexai"} {
		t.Run(provider, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, ": heartbeat\r\nid: event-1\r\nevent: message\r\n"+
					"data: {\"candidates\":[\r\ndata: {\"index\":0,\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"partial\"}]}}]}\r\n\r\n"+
					"event: error\r\ndata: {\"error\":{\"code\":429,\"message\":\"quota exceeded\",\"status\":\"RESOURCE_EXHAUSTED\"}}\r\n\r\n")
			}))
			t.Cleanup(server.Close)
			request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}}
			model := newGenerationChat(t, provider, server)
			text := ""
			var failure error
			for delta, err := range model.(chat.Streamer).Stream(t.Context(), request) {
				if err != nil {
					failure = err
					break
				}
				for _, part := range delta.Parts {
					if part.Kind == chat.PartDeltaText {
						text += part.Text
					}
				}
			}
			cause, found := errors.AsType[genai.APIError](failure)
			if text != "partial" || !found || cause.Code != 429 || cause.Status != "RESOURCE_EXHAUSTED" {
				t.Fatalf("text=%q error=%v; want partial text and native quota error", text, failure)
			}
		})
	}
}

func TestGenerationStreamPreservesBareNativeErrors(t *testing.T) {
	const nativeError = `{"error":{"code":429,"message":"quota exceeded","status":"RESOURCE_EXHAUSTED"}}`
	for _, provider := range []string{"google", "vertexai"} {
		for _, sample := range []struct {
			name, prefix string
		}{
			{"initial", ""},
			{"after partial output", "data: {\"candidates\":[{\"index\":0,\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"partial\"}]}}]}\n\n"},
			{"after terminal output", "data: {\"candidates\":[{\"index\":0,\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"answer\"}]},\"finishReason\":\"STOP\"}]}\r\n\r\n"},
		} {
			t.Run(provider+"/"+sample.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, sample.prefix+" \t"+nativeError)
				}))
				t.Cleanup(server.Close)
				request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}}
				model := newGenerationChat(t, provider, server)
				response, err := model.Call(t.Context(), request)
				cause, found := errors.AsType[genai.APIError](err)
				if response != nil || !found || cause.Code != 429 || cause.Status != "RESOURCE_EXHAUSTED" {
					t.Fatalf("Call = %#v, %v; want native quota error", response, err)
				}
				var failure error
				for delta, streamErr := range model.(chat.Streamer).Stream(t.Context(), request) {
					if streamErr != nil {
						failure = streamErr
						break
					}
					if delta.FinishReason != "" {
						t.Fatalf("failed generation completed: %#v", delta)
					}
				}
				cause, found = errors.AsType[genai.APIError](failure)
				if !found || cause.Code != 429 || cause.Status != "RESOURCE_EXHAUSTED" {
					t.Fatalf("Stream error = %v; want native quota error", failure)
				}
			})
		}
	}
}
