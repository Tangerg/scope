package ollama_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/models/ollama"
)

func TestNativeRequestsRejectNonSuccessStatusBeforeDecoding(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusNotModified, http.StatusBadRequest, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				writer.Header().Set("Location", "/redirected")
				writer.WriteHeader(status)
				if request.URL.Path == "/api/embed" {
					_, _ = writer.Write([]byte(`{"model":"model","embeddings":[[1,2]]}`))
					return
				}
				fmt.Fprintln(writer, `{"model":"model","message":{"role":"assistant","content":"answer"},"done":true,"done_reason":"stop"}`)
			}))
			t.Cleanup(server.Close)
			client := server.Client()
			client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			embeddings, err := ollama.NewEmbeddingModel(t.Context(), ollama.EmbeddingModelConfig{
				BaseURL: server.URL, HTTPClient: client, DefaultOptions: embedding.Options{Model: "model"},
			})
			if err != nil {
				t.Fatal(err)
			}
			conversation, err := ollama.NewChat(t.Context(), ollama.ChatConfig{
				BaseURL: server.URL, HTTPClient: client, DefaultOptions: chat.Options{Model: "model"},
			})
			if err != nil {
				t.Fatal(err)
			}
			request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}}
			for _, call := range []struct {
				name string
				run  func() error
			}{
				{name: "embedding", run: func() error {
					response, err := embeddings.Call(t.Context(), &embedding.Request{Texts: []string{"hello"}})
					if response != nil {
						t.Errorf("non-success HTTP status produced an embedding: %#v", response)
					}
					return err
				}},
				{name: "chat", run: func() error {
					response, err := conversation.Call(t.Context(), request)
					if response != nil {
						t.Errorf("non-success HTTP status produced a chat response: %#v", response)
					}
					return err
				}},
				{name: "stream", run: func() error {
					var failure error
					count := 0
					for delta, err := range conversation.Stream(t.Context(), request) {
						count++
						if delta != nil || err == nil {
							t.Errorf("non-success HTTP status produced a delta: %#v, %v", delta, err)
						}
						failure = err
					}
					if count != 1 {
						t.Errorf("stream yielded %d results, want one failure", count)
					}
					return failure
				}},
			} {
				t.Run(call.name, func(t *testing.T) {
					err := call.run()
					failure, ok := errors.AsType[interface {
						error
						HTTPStatus() int
					}](err)
					if !ok || failure.HTTPStatus() != status {
						t.Fatalf("error=%v; want inspectable HTTP status %d", err, status)
					}
				})
			}
		})
	}
}
