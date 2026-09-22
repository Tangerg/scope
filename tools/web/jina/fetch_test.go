package jina

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
		if r.Method != http.MethodPost || r.URL.Path != "/" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("X-Respond-With"); got != "text" {
			t.Errorf("X-Respond-With = %q", got)
		}
		if got := r.Header.Get("X-Return-Format"); got != "" {
			t.Errorf("legacy X-Return-Format = %q", got)
		}
		if got := r.Header.Get("X-Retain-Images"); got != "none" {
			t.Errorf("X-Retain-Images = %q", got)
		}
		var body struct {
			URL string `json:"url"`
		}
		if err := jsonv2.UnmarshalRead(r.Body, &body); err != nil {
			t.Errorf("decode body: %v", err)
			return
		}
		if body.URL != "https://example.com" {
			t.Errorf("url = %q", body.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"content":"example"}}`))
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(Config{APIKey: "test-key", FetchBaseURL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Fetch(t.Context(), &web.FetchRequest{URL: "https://example.com", Format: web.FormatText})
	if err != nil {
		t.Fatal(err)
	}
	if response.Format != web.FormatText || response.Content != "example" {
		t.Fatalf("response = %#v", response)
	}
}
