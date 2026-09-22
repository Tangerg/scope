package tavily

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tangerg/scope/tools/web"
)

func TestFetch(t *testing.T) {
	if _, err := NewClient(Config{}); err == nil {
		t.Fatal("NewClient accepted an empty API key")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/extract" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		var body struct {
			URLs   []string `json:"urls"`
			Depth  string   `json:"extract_depth"`
			Format string   `json:"format"`
		}
		if err := jsonv2.UnmarshalRead(r.Body, &body); err != nil {
			t.Errorf("decode body: %v", err)
			return
		}
		if len(body.URLs) != 1 || body.URLs[0] != "https://example.com" || body.Depth != "basic" || body.Format != "markdown" {
			t.Errorf("body = %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"raw_content":"example"}],"failed_results":[]}`))
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(Config{APIKey: "test-key", BaseURL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Fetch(t.Context(), &web.FetchRequest{URL: "https://example.com", Format: web.FormatMarkdown})
	if err != nil {
		t.Fatal(err)
	}
	if response.Format != web.FormatMarkdown || response.Content != "example" {
		t.Fatalf("response = %#v", response)
	}
	if _, err := client.Fetch(t.Context(), &web.FetchRequest{URL: "https://example.com", Format: web.FormatHTML}); !errors.Is(err, web.ErrUnsupportedFormat) {
		t.Fatalf("HTML fetch error = %v, want ErrUnsupportedFormat", err)
	}
}
