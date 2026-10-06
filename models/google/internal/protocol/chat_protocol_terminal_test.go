package protocol_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/genai"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/google/internal/protocol"
)

func TestChat_RejectsGenerationAfterNativeFinish(t *testing.T) {
	for _, provider := range []string{"google", "vertexai"} {
		for _, tc := range []struct {
			name string
			tail string
		}{
			{"text", `{"candidates":[{"index":0,"content":{"parts":[{"text":"late"}]}}]}`},
			{"reasoning", `{"candidates":[{"index":0,"content":{"parts":[{"text":"late","thought":true}]}}]}`},
			{"tool", `{"candidates":[{"index":0,"content":{"parts":[{"functionCall":{"name":"lookup","args":{}}}]}}]}`},
			{"media", `{"candidates":[{"index":0,"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"aW1hZ2U="}}]}}]}`},
			{"duplicate_finish", `{"candidates":[{"index":0,"finishReason":"MAX_TOKENS"}]}`},
		} {
			t.Run(provider+"/"+tc.name, func(t *testing.T) {
				adapter := newTerminalChat(t, provider, tc.tail)
				request := &corechat.Request{Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hello"))}}
				response, err := adapter.Call(t.Context(), request)
				if response != nil || !errors.Is(err, corechat.ErrInvalidResponse) {
					t.Errorf("Call: response = %#v, error = %v; want invalid response", response, err)
				}
				var streamErr error
				for delta, err := range adapter.Stream(t.Context(), request) {
					if err != nil {
						streamErr = err
						break
					}
					if delta.FinishReason != "" {
						t.Error("Stream published a successful terminal delta")
					}
				}
				if !errors.Is(streamErr, corechat.ErrInvalidResponse) {
					t.Errorf("Stream: error = %v; want invalid response", streamErr)
				}
			})
		}
	}
}

func TestChat_PreservesMetadataAfterNativeFinish(t *testing.T) {
	for _, provider := range []string{"google", "vertexai"} {
		t.Run(provider, func(t *testing.T) {
			citationField := "citationSources"
			if provider == "vertexai" {
				citationField = "citations"
			}
			adapter := newTerminalChat(t, provider, fmt.Sprintf(`{"candidates":[{"index":0,"finishReason":"FINISH_REASON_UNSPECIFIED","finishMessage":"complete","citationMetadata":{"%s":[{"uri":"https://example.com/source","title":"Source"}]}}]}`, citationField))
			request := &corechat.Request{Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hello"))}}
			response, err := adapter.Call(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			checkTerminalChatResponse(t, provider, response)
			var accumulator corechat.ResponseAccumulator
			for delta, err := range adapter.Stream(t.Context(), request) {
				if err != nil {
					t.Fatal(err)
				}
				if err := accumulator.Add(delta); err != nil {
					t.Fatal(err)
				}
			}
			response, err = accumulator.Response()
			if err != nil {
				t.Fatal(err)
			}
			checkTerminalChatResponse(t, provider, response)
		})
	}
}

func newTerminalChat(t *testing.T, provider, tail string) *protocol.Chat {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range []string{
			`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"answer"}]},"finishReason":"STOP"}]}`,
			tail,
			`{"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3}}`,
		} {
			fmt.Fprintf(writer, "data: %s\n\n", chunk)
		}
	}))
	t.Cleanup(server.Close)
	client := protocol.ClientConfig{APIKey: "test-key", BaseURL: server.URL}
	if provider == "vertexai" {
		client = protocol.ClientConfig{
			Backend: genai.BackendVertexAI, Project: "test-project", Location: "us-central1",
			BaseURL: server.URL, HTTPClient: server.Client(),
		}
	}
	adapter, err := protocol.NewChat(t.Context(), protocol.ChatConfig{
		Provider: provider, Client: client,
		DefaultOptions: corechat.Options{Model: "gemini-3-pro"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func checkTerminalChatResponse(t *testing.T, provider string, response *corechat.Response) {
	t.Helper()
	if response.Output.FinishReason != corechat.FinishReasonStop {
		t.Errorf("finish = %s, want stop", response.Output.FinishReason)
	}
	parts := response.Output.Message.Parts
	if len(parts) != 1 || parts[0].Text != "answer" {
		t.Fatalf("parts = %#v, want one answer text part", parts)
	}
	if citations := parts[0].Citations; len(citations) != 1 || citations[0].Source.Value != "https://example.com/source" || citations[0].Title != "Source" {
		t.Errorf("citations = %#v", citations)
	}
	if usage := response.Metadata.Usage; usage == nil || usage.InputTokens != 1 || usage.OutputTokens != 2 {
		t.Errorf("usage = %#v, want 1 input and 2 output tokens", usage)
	}
	native, found, err := response.Output.Metadata.Extra.Decode[genai.FinishReason](provider + "/native_finish_reason")
	if err != nil || !found || native != genai.FinishReasonStop {
		t.Errorf("native finish = %s, found %v, error = %v; want STOP", native, found, err)
	}
}
