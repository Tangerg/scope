package brave

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	toolcontract "github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/tools/web"
)

func TestSearchRejectsFinalNativeQueryBeforeHTTP(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"web":{"results":[]}}`))
	}))
	defer server.Close()
	client, err := NewClient(t.Context(), Config{APIKey: "test", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []*web.SearchRequest{
		{Query: strings.Repeat("a", maximumQueryCharacters+1)},
		{Query: strings.Repeat("word ", maximumQueryWords+1)},
		{Query: strings.Repeat("a", maximumQueryCharacters-5), AllowedDomains: []string{"example.com"}},
	} {
		_, err := client.Search(t.Context(), request)
		failure, definite := toolcontract.ClassifyFailure(err)
		if !definite || failure.Kind() != toolcontract.FailureKindFailed || !errors.Is(failure.Cause(), web.ErrInvalidQuery) {
			t.Fatalf("unsupported final query admitted: %v", err)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid queries performed %d HTTP calls", calls)
	}
	if _, err := client.Search(t.Context(), &web.SearchRequest{Query: strings.Repeat("字", maximumQueryCharacters)}); err != nil {
		t.Fatalf("character limit was interpreted as byte limit: %v", err)
	}
	if calls != 1 {
		t.Fatalf("valid query calls = %d", calls)
	}
}
