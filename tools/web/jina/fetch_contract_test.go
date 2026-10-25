package jina_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Tangerg/scope/tools/web"
	"github.com/Tangerg/scope/tools/web/jina"
)

type fetchTransport struct {
	t        *testing.T
	format   web.ContentFormat
	response string
	calls    int
}

func (f *fetchTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	f.calls++
	if request.Method != http.MethodPost || request.URL.String() != "https://reader.example/" {
		f.t.Errorf("request = %s %s", request.Method, request.URL)
	}
	if request.Header.Get("Accept") != "application/json" || request.Header.Get("X-Respond-With") != string(f.format) {
		f.t.Errorf("response selection headers = %v", request.Header)
	}
	var body struct {
		URL string `json:"url"`
	}
	if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
		f.t.Errorf("request body: %v", err)
	}
	_ = request.Body.Close()
	if body.URL != "https://article.example/page" {
		f.t.Errorf("fetch target = %q", body.URL)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(f.response)),
		Request:    request,
	}, nil
}

func TestFetchUsesTheRequestedNativeResponseField(t *testing.T) {
	for _, test := range []struct {
		name      string
		format    web.ContentFormat
		body      string
		content   string
		wantError bool
	}{
		{name: "default markdown", body: `{"data":{"content":"# article"}}`, content: "# article"},
		{name: "markdown", format: web.FormatMarkdown, body: `{"data":{"content":"# article"}}`, content: "# article"},
		{name: "html", format: web.FormatHTML, body: `{"data":{"html":"<h1>article</h1>"}}`, content: "<h1>article</h1>"},
		{name: "text", format: web.FormatText, body: `{"data":{"text":"article"}}`, content: "article"},
		{name: "empty markdown", format: web.FormatMarkdown, body: `{"data":{"content":""}}`},
		{name: "empty html", format: web.FormatHTML, body: `{"data":{"html":""}}`},
		{name: "empty text", format: web.FormatText, body: `{"data":{"text":""}}`},
		{name: "missing markdown", format: web.FormatMarkdown, body: `{"data":{"html":"wrong","text":"wrong"}}`, wantError: true},
		{name: "missing html", format: web.FormatHTML, body: `{"data":{"content":"wrong","text":"wrong"}}`, wantError: true},
		{name: "missing text", format: web.FormatText, body: `{"data":{"content":"wrong","html":"wrong"}}`, wantError: true},
		{name: "null markdown", format: web.FormatMarkdown, body: `{"data":{"content":null,"html":"wrong","text":"wrong"}}`, wantError: true},
		{name: "null html", format: web.FormatHTML, body: `{"data":{"html":null,"content":"wrong","text":"wrong"}}`, wantError: true},
		{name: "null text", format: web.FormatText, body: `{"data":{"text":null,"content":"wrong","html":"wrong"}}`, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			format := test.format
			if format == "" {
				format = web.FormatMarkdown
			}
			transport := &fetchTransport{t: t, format: format, response: test.body}
			client, err := jina.NewClient(t.Context(), jina.Config{
				APIKey: "test-key", FetchBaseURL: "https://reader.example", HTTPClient: &http.Client{Transport: transport},
			})
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.Fetch(t.Context(), &web.FetchRequest{URL: "https://article.example/page", Format: test.format})
			if transport.calls != 1 {
				t.Fatalf("transport calls = %d", transport.calls)
			}
			if test.wantError {
				if !errors.Is(err, web.ErrInvalidFetchResponse) || response != nil {
					t.Fatalf("invalid native shape: response=%+v error=%v", response, err)
				}
				return
			}
			if err != nil || response == nil || response.Format != format || response.Content != test.content {
				t.Fatalf("native format %s: response=%+v error=%v", format, response, err)
			}
		})
	}
}
