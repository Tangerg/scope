package exa

import (
	jsonv2 "encoding/json/v2"
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
		if r.Method != http.MethodPost || r.URL.Path != "/contents" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("x-api-key"); got != "test-key" {
			t.Errorf("x-api-key = %q", got)
		}
		var body struct {
			URLs []string `json:"urls"`
			Text struct {
				IncludeHTMLTags bool `json:"includeHtmlTags"`
			} `json:"text"`
		}
		if err := jsonv2.UnmarshalRead(r.Body, &body); err != nil {
			t.Errorf("decode body: %v", err)
			return
		}
		if len(body.URLs) != 1 || body.URLs[0] != "https://example.com" || !body.Text.IncludeHTMLTags {
			t.Errorf("body = %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"text":"<main>example</main>"}]}`))
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(Config{APIKey: "test-key", BaseURL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Fetch(t.Context(), &web.FetchRequest{URL: "https://example.com", Format: web.FormatHTML})
	if err != nil {
		t.Fatal(err)
	}
	if response.Format != web.FormatHTML || response.Content != "<main>example</main>" {
		t.Fatalf("response = %#v", response)
	}
}
