package exa

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	toolcontract "github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/tools/web"
)

func TestFetchRejectsUnsupportedPlainTextBeforeIO(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("unsupported format reached provider")
	}))
	t.Cleanup(server.Close)
	client, err := NewClient(t.Context(), Config{APIKey: "test-key", BaseURL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Fetch(t.Context(), &web.FetchRequest{URL: "https://example.com", Format: web.FormatText})
	failure, definite := toolcontract.ClassifyFailure(err)
	if response != nil || !definite || failure.Kind() != toolcontract.FailureKindFailed || !errors.Is(failure.Cause(), web.ErrUnsupportedFormat) {
		t.Fatalf("response = %#v, error = %v", response, err)
	}
}
