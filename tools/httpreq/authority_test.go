package httpreq

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/core/tool"
)

func TestRequestRejectsEmptyHostname(t *testing.T) {
	for _, address := range []string{"http://:80/page", "https://:443/page"} {
		request := &Request{URL: address}
		err := request.Validate()
		rejection, public := tool.ClassifyInputRejection(err)
		if !public || !errors.Is(rejection.Cause(), ErrInvalidURL) {
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

func TestRedirectKeepsHostVetoAndBindsSecretsToOriginalOrigin(t *testing.T) {
	ctx := t.Context()
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("X-Private-Key") != "" || r.Header.Get("Authorization") != "" {
			t.Error("cross-origin request received credentials")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Private-Key") != "secret" {
			t.Error("missing source credential")
		}
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	denied := errors.New("host refused redirect")
	cfg := ClientConfig{AllowedHosts: []string{"127.0.0.1"}, AllowedMethods: []Method{MethodGET, MethodPOST}, DefaultHeaders: map[string]string{"X-Private-Key": "secret"}, HTTPClient: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return denied }}}
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, callErr := client.Do(ctx, &Request{URL: source.URL}); !errors.Is(callErr, denied) || hits.Load() != 0 {
		t.Fatalf("host veto: hits=%d err=%v", hits.Load(), callErr)
	}
	cfg.HTTPClient.CheckRedirect = nil
	client, err = NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, callErr := client.Do(ctx, &Request{URL: source.URL, Headers: map[string]string{"Authorization": "Bearer test"}}); callErr != nil || hits.Load() != 1 {
		t.Fatalf("redirect: hits=%d err=%v", hits.Load(), callErr)
	}
	if _, callErr := client.Do(ctx, &Request{URL: source.URL, Method: MethodPOST, Body: "secret payload"}); !errors.Is(callErr, ErrRedirectBody) || hits.Load() != 1 {
		t.Fatalf("cross-origin body: hits=%d err=%v", hits.Load(), callErr)
	}
	cfg.HTTPClient.CheckRedirect = func(r *http.Request, _ []*http.Request) error { r.URL.Host = "not-authorized.invalid"; return nil }
	client, err = NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, callErr := client.Do(ctx, &Request{URL: source.URL}); !errors.Is(callErr, ErrHostNotAllowed) || hits.Load() != 1 {
		t.Fatal("callback expanded network authority", callErr)
	}
}
