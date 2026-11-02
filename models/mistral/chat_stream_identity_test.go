package mistral_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/mistral"
)

func TestChatRejectsNativeStreamIdentityChanges(t *testing.T) {
	for _, identity := range []string{
		`"id":"different","model":"mistral-actual"`,
		`"id":"response-1","model":"mistral-different"`,
	} {
		for _, afterFinish := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/after-finish=%t", identity, afterFinish), func(t *testing.T) {
				first := `{"id":"response-1","model":"mistral-actual","choices":[{"index":0,"delta":{"content":"answer"}}]}`
				tail := `{` + identity + `,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`
				if afterFinish {
					first = `{"id":"response-1","model":"mistral-actual","choices":[{"index":0,"delta":{"content":"answer"},"finish_reason":"stop"}]}`
					tail = `{` + identity + `,"choices":[],"usage":{"prompt_tokens":1}}`
				}
				model := newIdentityChat(t, first, tail)
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
			})
		}
	}
}

func TestChatBindsLateNativeStreamIdentityAndPreservesItWhenOmitted(t *testing.T) {
	model := newIdentityChat(t,
		`{"choices":[{"index":0,"delta":{"content":"a"}}]}`,
		`{"id":"response-1","model":"mistral-actual","choices":[{"index":0,"delta":{"content":"b"}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2}}`,
	)
	request := &corechat.Request{Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hello"))}}
	response, err := model.Call(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	assertMistralStreamIdentity(t, response)
	response, err = corechat.Collect(model.Stream(t.Context(), request))
	if err != nil {
		t.Fatal(err)
	}
	assertMistralStreamIdentity(t, response)
}

func newIdentityChat(t *testing.T, chunks ...string) *mistral.Chat {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range chunks {
			fmt.Fprintf(writer, "data: %s\n\n", chunk)
		}
		fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)
	model, err := mistral.NewChat(t.Context(), mistral.ChatConfig{
		APIKey: "test-key", BaseURL: server.URL, DefaultOptions: corechat.Options{Model: "mistral-alias"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return model
}

func assertMistralStreamIdentity(t *testing.T, response *corechat.Response) {
	t.Helper()
	if response.Text() != "ab" || response.Metadata.ID != "response-1" || response.Metadata.Model != "mistral-actual" || response.Output.FinishReason != corechat.FinishReasonStop {
		t.Fatalf("response = %#v, metadata = %#v, text = %q", response, response.Metadata, response.Text())
	}
}
