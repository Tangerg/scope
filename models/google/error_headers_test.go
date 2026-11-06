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

func TestNativeGenerationPreservesErrorHeaders(t *testing.T) {
	for _, provider := range []string{"google", "vertexai"} {
		for _, test := range []struct {
			name, body string
			sdkCause   bool
			status     int
		}{
			{"API envelope", `{"error":{"code":429,"message":"quota exceeded","status":"RESOURCE_EXHAUSTED"}}`, true, http.StatusTooManyRequests},
			{"malformed envelope", `{"error":`, false, http.StatusTooManyRequests},
			{"error frame", `{"error":{"code":429,"message":"quota exceeded","status":"RESOURCE_EXHAUSTED"}}`, true, http.StatusOK},
		} {
			t.Run(provider+"/"+test.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					writer.Header().Set("Content-Type", "application/json")
					writer.Header().Set("Retry-After", "17")
					writer.Header().Set("X-Request-Id", "request-123")
					if test.status == http.StatusOK && request.URL.Query().Get("alt") == "sse" {
						writer.Header().Set("Content-Type", "text/event-stream")
						_, _ = fmt.Fprintf(writer, "data: %s\n\n", test.body)
						return
					}
					writer.WriteHeader(test.status)
					_, _ = fmt.Fprint(writer, test.body)
				}))
				defer server.Close()
				assertError := func(t *testing.T, err error) {
					t.Helper()
					var classified interface {
						HTTPStatus() int
						HTTPHeader() http.Header
					}
					if !errors.As(err, &classified) || classified.HTTPStatus() != http.StatusTooManyRequests {
						t.Fatalf("classification=%v", err)
					}
					headers := classified.HTTPHeader()
					if headers.Get("Retry-After") != "17" || headers.Get("X-Request-Id") != "request-123" {
						t.Fatalf("headers=%v", headers)
					}
					headers["Retry-After"][0] = "0"
					headers.Del("X-Request-Id")
					if classified.HTTPHeader().Get("Retry-After") != "17" || classified.HTTPHeader().Get("X-Request-Id") != "request-123" {
						t.Fatal("caller mutated saved headers")
					}
					if test.sdkCause {
						cause, ok := errors.AsType[genai.APIError](err)
						if !ok || cause.Code != 429 || cause.Status != "RESOURCE_EXHAUSTED" || cause.Message != "quota exceeded" {
							t.Fatalf("SDK cause=%#v present=%t", cause, ok)
						}
					}
				}
				t.Run("stream", func(t *testing.T) {
					request, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("hello")))
					if err != nil {
						t.Fatal(err)
					}
					failures := 0
					for response, err := range newNativeChat(t, provider, server).Stream(t.Context(), request) {
						if response != nil {
							t.Fatalf("response=%v", response)
						}
						assertError(t, err)
						failures++
					}
					if failures != 1 {
						t.Fatalf("failures=%d", failures)
					}
				})
				t.Run("unary", func(t *testing.T) {
					response, err := newNativeTranscription(t, provider, server).Call(t.Context(), nativeTranscriptionRequest(t))
					if response != nil {
						t.Fatalf("response=%v", response)
					}
					assertError(t, err)
				})
			})
		}
	}
}
