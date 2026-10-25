package protocol_test

import (
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/google/internal/protocol"
)

func TestTokenCounterRequiresReportedCount(t *testing.T) {
	for _, body := range []string{`{}`, `{"totalTokens":null}`, `{"totalTokens":0}`} {
		t.Run(body, func(t *testing.T) {
			calls := 0
			server := modeltest.JSONServer(http.StatusOK, body, func(*http.Request) { calls++ })
			defer server.Close()
			counter, err := protocol.NewTextCounter(t.Context(), protocol.TextCounterConfig{Client: protocol.ClientConfig{APIKey: "test", BaseURL: server.URL}, Model: "gemini-3.6-flash"})
			if err != nil {
				t.Fatal(err)
			}
			count, err := counter.CountText(t.Context(), "text")
			if calls != 1 {
				t.Fatal(calls)
			}
			if body != `{"totalTokens":0}` {
				if err == nil {
					t.Fatalf("unreported count=%d", count)
				}
				return
			}
			if err != nil || count != 0 {
				t.Fatalf("count=%d error=%v", count, err)
			}
		})
	}
}
