package google_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tangerg/scope/core/transcription"
)

func TestNativeTranscriptionRequiresReportedText(t *testing.T) {
	for _, provider := range []string{"google", "vertexai"} {
		for _, parts := range []string{`[]`, `[{}]`, `[{"text":null}]`, `[{"thought":true,"text":"internal reasoning"}]`, `[{"text":""}]`} {
			t.Run(provider+parts, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprintf(w, `{"candidates":[{"content":{"parts":%s},"finishReason":"STOP"}]}`, parts)
				}))
				defer server.Close()
				response, err := newNativeTranscription(t, provider, server).Call(t.Context(), nativeTranscriptionRequest(t))
				if parts != `[{"text":""}]` {
					if !errors.Is(err, transcription.ErrInvalidResponse) {
						t.Fatalf("response=%v error=%v", response, err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if response.Output.Text != "" {
					t.Fatalf("text=%q", response.Output.Text)
				}
			})
		}
	}
}
