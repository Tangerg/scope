package tavily

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	toolcontract "github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/tools/web"
)

func TestSearchChecksNativeCharacterLimitBeforeHTTP(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()
	client, err := NewClient(t.Context(), Config{APIKey: "test", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Search(t.Context(), &web.SearchRequest{Query: strings.Repeat("a", maximumQueryCharacters+1)})
	failure, definite := toolcontract.ClassifyFailure(err)
	if !definite || failure.Kind() != toolcontract.FailureKindFailed || !errors.Is(failure.Cause(), web.ErrInvalidQuery) || calls != 0 {
		t.Fatalf("unsupported query reached provider: calls=%d error=%v", calls, err)
	}
	if _, err := client.Search(t.Context(), &web.SearchRequest{Query: strings.Repeat("字", maximumQueryCharacters)}); err != nil || calls != 1 {
		t.Fatalf("valid Unicode query rejected: calls=%d error=%v", calls, err)
	}
}
