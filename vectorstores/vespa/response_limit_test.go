package vespa

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSendJSONRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("oversized"))
	}))
	t.Cleanup(server.Close)

	store := &Store{endpoint: server.URL, httpClient: server.Client(), maxResponseBytes: 4}
	_, _, err := store.sendJSON(t.Context(), http.MethodGet, "/", nil)
	if err == nil || !strings.Contains(err.Error(), "4-byte limit") {
		t.Fatalf("sendJSON error = %v", err)
	}
}
