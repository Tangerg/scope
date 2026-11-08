package providerhttp

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/go-resty/resty/v2"
)

type fixtureTransport func(*http.Request) (*http.Response, error)

func (f fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type blankStream struct{}

func (blankStream) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = ' '
	}
	return len(p), nil
}

func TestNativeFailuresAreBoundedAndCannotEchoSecrets(t *testing.T) {
	const sensitive = "private-credential-in-remote-diagnostic"
	for _, tc := range []struct {
		name   string
		status int
		body   io.Reader
		want   error
	}{
		{"null", 200, strings.NewReader("  null "), errEmptyBody},
		{"status", 403, strings.NewReader(sensitive), nil},
		{"stream", 200, io.LimitReader(blankStream{}, maxResponseBytes+1), resty.ErrResponseBodyTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, buildErr := NewClient(t.Context(), Config{BaseURL: "https://search.example", HTTPClient: &http.Client{Transport: fixtureTransport(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(tc.body), Request: r, ContentLength: -1}, nil
			})}})
			if buildErr != nil {
				t.Fatal(buildErr)
			}
			err := Execute(c.R().SetContext(t.Context()), http.MethodGet, "/search", new(struct{}))
			cause := err
			if diagnostic, ok := errors.AsType[*exchangeError](err); ok {
				cause = diagnostic.Cause()
			}
			if err == nil || strings.Contains(err.Error(), sensitive) || tc.want != nil && !errors.Is(cause, tc.want) {
				t.Fatalf("native failure = %v", err)
			}
		})
	}
}
