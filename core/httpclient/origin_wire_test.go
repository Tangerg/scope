package httpclient_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/httpclient"
)

func TestOriginGuardUsesTheAuthorityWrittenToTheWire(t *testing.T) {
	const (
		originURL  = "http://api.example"
		credential = "Bearer private-credential"
		payload    = "private request"
	)
	for _, test := range []struct {
		name    string
		opaque  string
		allowed bool
	}{
		{name: "escaped hierarchical path", allowed: true},
		{name: "opaque authority", opaque: "//other.example/private"},
		{name: "opaque absolute URI", opaque: "http://other.example/private"},
		{name: "opaque path", opaque: "/value%2Fone"},
	} {
		t.Run(test.name, func(t *testing.T) {
			type observation struct {
				target        string
				host          string
				authorization string
				body          string
			}
			received := make(chan observation, 1)
			proxy := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				body, err := io.ReadAll(request.Body)
				if err != nil {
					t.Errorf("read proxy request: %v", err)
				}
				received <- observation{
					target: request.RequestURI, host: request.Host,
					authorization: request.Header.Get("Authorization"), body: string(body),
				}
				writer.WriteHeader(http.StatusNoContent)
			}))
			defer proxy.Close()
			proxyURL, err := url.Parse(proxy.URL)
			if err != nil {
				t.Fatal(err)
			}
			transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
			defer transport.CloseIdleConnections()
			client, err := httpclient.New(t.Context(), httpclient.Config{
				BaseURL: originURL, HTTPClient: &http.Client{Transport: transport},
			})
			if err != nil {
				t.Fatal(err)
			}
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, originURL+"/value%2Fone", strings.NewReader(payload))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Authorization", credential)
			request.URL.Opaque = test.opaque
			response, requestErr := client.Do(request)
			if response != nil {
				_ = response.Body.Close()
			}
			if !test.allowed {
				select {
				case actual := <-received:
					t.Fatalf("unauthorized request reached proxy: %+v; error=%v", actual, requestErr)
				default:
				}
				if !errors.Is(requestErr, httpclient.ErrOriginNotAllowed) {
					t.Fatalf("opaque target was not rejected at the origin boundary: %v", requestErr)
				}
				return
			}
			if requestErr != nil {
				t.Fatal(requestErr)
			}
			actual := <-received
			if actual.target != originURL+"/value%2Fone" || actual.host != "api.example" || actual.authorization != credential || actual.body != payload {
				t.Fatalf("authorized escaped request changed: %+v", actual)
			}
		})
	}
}
