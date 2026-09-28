package httpreq

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestRejectsEmptyHostname(t *testing.T) {
	for _, address := range []string{"http://:80/page", "https://:443/page"} {
		request := &Request{URL: address}
		if err := request.Validate(); !errors.Is(err, ErrInvalidURL) {
			t.Errorf("Validate(%q) = %v, want ErrInvalidURL", address, err)
		}
	}
}

func TestModelCannotOverrideHTTPAuthority(t *testing.T) {
	for _, name := range []string{"Host", "host", "hOsT"} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Error("request with forbidden authority reached the transport")
			}))
			t.Cleanup(server.Close)
			client, err := NewClient(ClientConfig{AllowedHosts: []string{"127.0.0.1"}, HTTPClient: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.Do(t.Context(), &Request{URL: server.URL, Headers: map[string]string{name: "admin.invalid"}})
			if response != nil || !errors.Is(err, ErrHostHeaderOverride) {
				t.Fatalf("response = %#v, error = %v", response, err)
			}
		})
	}
}

func TestTrustedDefaultAuthorityRemainsHostOwned(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "trusted.internal" {
			t.Errorf("authority = %q", r.Host)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	client, err := NewClient(ClientConfig{AllowedHosts: []string{"127.0.0.1"}, DefaultHeaders: map[string]string{"Host": "trusted.internal"}, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Do(t.Context(), &Request{URL: server.URL}); err != nil {
		t.Fatal(err)
	}
}
