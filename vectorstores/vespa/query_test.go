package vespa

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestQueryRequiresFullCoverage(t *testing.T) {
	for name, body := range map[string]string{
		"missing":            `{"root":{"children":[]}}`,
		"timeout":            `{"root":{"coverage":{"coverage":99,"full":false,"degraded":{"timeout":true}}}}`,
		"contradictory full": `{"root":{"coverage":{"coverage":100,"full":true,"degraded":{"new-reason":true}}}}`,
		"incomplete percent": `{"root":{"coverage":{"coverage":80,"full":true}}}`,
		"reported error":     `{"root":{"coverage":{"coverage":100,"full":true},"errors":[{"code":12,"summary":"Timed out","message":"timeout"}]}}`,
		"invalid json":       `{`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
			t.Cleanup(server.Close)
			store := &Store{endpoint: server.URL, httpClient: server.Client()}
			if hits, err := store.query(t.Context(), map[string]any{"yql": "select * from scope where true"}); err == nil || hits != nil {
				t.Fatalf("incomplete query: hits=%v error=%v", hits, err)
			}
		})
	}
}
