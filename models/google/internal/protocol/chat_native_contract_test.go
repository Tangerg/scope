package protocol_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"google.golang.org/genai"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/google/internal/protocol"
)

func TestChatRejectsCodeExecutionBeforeIO(t *testing.T) {
	for _, provider := range []string{"google", "vertexai"} {
		for _, configuration := range []string{`{}`, `null`} {
			t.Run(provider+"/"+configuration, func(t *testing.T) {
				var calls atomic.Int32
				model := newNativeContractChat(t, provider, func(writer http.ResponseWriter, _ *http.Request) {
					calls.Add(1)
					writer.WriteHeader(http.StatusBadRequest)
				})
				request := &corechat.Request{Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("calculate"))}}
				extension := json.RawMessage(`{"tools":[{"codeExecution":` + configuration + `}]}`)
				if err := request.Options.Extensions.Set(provider+"/request", extension); err != nil {
					t.Fatal(err)
				}
				if response, err := model.Call(t.Context(), request); response != nil || !errors.Is(err, errors.ErrUnsupported) {
					t.Errorf("Call: response = %#v, error = %v; want unsupported capability", response, err)
				}
				errorsReported := 0
				for delta, err := range model.Stream(t.Context(), request) {
					if delta != nil || !errors.Is(err, errors.ErrUnsupported) {
						t.Errorf("Stream: delta = %#v, error = %v; want unsupported capability", delta, err)
					}
					errorsReported++
				}
				if errorsReported != 1 || calls.Load() != 0 {
					t.Fatalf("stream errors = %d, HTTP calls = %d; want one error and no I/O", errorsReported, calls.Load())
				}
			})
		}
	}
}

func TestChatRejectsUnrepresentableNativeParts(t *testing.T) {
	for _, provider := range []string{"google", "vertexai"} {
		for _, part := range []string{
			`{"executableCode":{"language":"PYTHON","code":"print(1 + 1)"}}`,
			`{"codeExecutionResult":{"outcome":"OUTCOME_OK","output":"2"}}`,
			`{"text":"answer","executableCode":{"language":"PYTHON","code":"print(1 + 1)"}}`,
		} {
			t.Run(provider+"/"+part, func(t *testing.T) {
				model := newNativeContractChat(t, provider, nativeChatChunks(
					`{"candidates":[{"index":0,"content":{"parts":[{"text":"working"}]}}]}`,
					`{"candidates":[{"index":0,"content":{"parts":[`+part+`]},"finishReason":"STOP"}]}`,
				))
				assertInvalidNativeChat(t, model)
			})
		}
	}
}

func TestChatRejectsNativeIdentityChanges(t *testing.T) {
	for _, provider := range []string{"google", "vertexai"} {
		for _, changedIdentity := range []string{
			`"responseId":"different","modelVersion":"gemini-actual"`,
			`"responseId":"response-1","modelVersion":"gemini-different"`,
		} {
			for _, afterFinish := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/after-finish=%t", provider, changedIdentity, afterFinish), func(t *testing.T) {
					first := `{"responseId":"response-1","modelVersion":"gemini-actual","candidates":[{"index":0,"content":{"parts":[{"text":"answer"}]}}]}`
					tail := `{` + changedIdentity + `,"candidates":[{"index":0,"finishReason":"STOP"}]}`
					if afterFinish {
						first = `{"responseId":"response-1","modelVersion":"gemini-actual","candidates":[{"index":0,"content":{"parts":[{"text":"answer"}]},"finishReason":"STOP"}]}`
						tail = `{` + changedIdentity + `,"usageMetadata":{"promptTokenCount":1}}`
					}
					model := newNativeContractChat(t, provider, nativeChatChunks(first, tail))
					assertInvalidNativeChat(t, model)
				})
			}
		}
	}
}

func TestChatBindsLateNativeIdentityAndPreservesItWhenOmitted(t *testing.T) {
	for _, provider := range []string{"google", "vertexai"} {
		t.Run(provider, func(t *testing.T) {
			model := newNativeContractChat(t, provider, nativeChatChunks(
				`{"candidates":[{"index":0,"content":{"parts":[{"text":"a"}]}}]}`,
				`{"responseId":"response-1","modelVersion":"gemini-actual","candidates":[{"index":0,"content":{"parts":[{"text":"b"}]}}]}`,
				`{"candidates":[{"index":0,"finishReason":"STOP"}]}`,
				`{"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2}}`,
			))
			request := &corechat.Request{Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hello"))}}
			response, err := model.Call(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			assertNativeChatIdentity(t, response)
			response, err = corechat.Collect(model.Stream(t.Context(), request))
			if err != nil {
				t.Fatal(err)
			}
			assertNativeChatIdentity(t, response)
		})
	}
}

func newNativeContractChat(t *testing.T, provider string, handler http.HandlerFunc) *protocol.Chat {
	t.Helper()
	model, err := protocol.NewChat(t.Context(), protocol.ChatConfig{
		Provider: provider, Client: newNativeContractClient(t, provider, handler), DefaultOptions: corechat.Options{Model: "gemini-alias"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return model
}

func newNativeContractClient(t *testing.T, provider string, handler http.HandlerFunc) protocol.ClientConfig {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := protocol.ClientConfig{APIKey: "test-key", BaseURL: server.URL, HTTPClient: server.Client()}
	if provider == "vertexai" {
		client = protocol.ClientConfig{
			Backend: genai.BackendVertexAI, Project: "test-project", Location: "us-central1",
			BaseURL: server.URL, HTTPClient: server.Client(),
		}
	}
	return client
}

func nativeChatChunks(chunks ...string) http.HandlerFunc {
	return func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range chunks {
			fmt.Fprintf(writer, "data: %s\n\n", chunk)
		}
	}
}

func assertInvalidNativeChat(t *testing.T, model *protocol.Chat) {
	t.Helper()
	request := &corechat.Request{Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hello"))}}
	if response, err := model.Call(t.Context(), request); response != nil || !errors.Is(err, corechat.ErrInvalidResponse) {
		t.Errorf("Call: response = %#v, error = %v; want invalid response", response, err)
	}
	var streamErr error
	for delta, err := range model.Stream(t.Context(), request) {
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
}

func assertNativeChatIdentity(t *testing.T, response *corechat.Response) {
	t.Helper()
	if response.Text() != "ab" || response.Metadata.ID != "response-1" || response.Metadata.Model != "gemini-actual" || response.Output.FinishReason != corechat.FinishReasonStop {
		t.Fatalf("response = %#v, metadata = %#v, text = %q", response, response.Metadata, response.Text())
	}
}
