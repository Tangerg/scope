package exa

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tangerg/scope/tools/web"
)

func TestFetchRejectsUnsupportedPlainTextBeforeIO(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("unsupported format reached provider")
	}))
	t.Cleanup(server.Close)
	client, err := NewClient(Config{APIKey: "test-key", BaseURL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Fetch(t.Context(), &web.FetchRequest{URL: "https://example.com", Format: web.FormatText})
	if response != nil || !errors.Is(err, web.ErrUnsupportedFormat) {
		t.Fatalf("response = %#v, error = %v", response, err)
	}
}
