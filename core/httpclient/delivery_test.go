package httpclient_test

import (
	"errors"
	"net/http"
	"net/http/cookiejar"
	"testing"

	"github.com/Tangerg/scope/core/httpclient"
)

func TestDeliveryClientSendsCredentialsOnlyToItsCredentialOrigin(t *testing.T) {
	api, err := httpclient.ParseOrigin("https://api.example")
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := httpclient.ParseOrigin("https://cdn.example")
	if err != nil {
		t.Fatal(err)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	received := map[string]http.Header{}
	host := &http.Client{Jar: jar, Transport: &recordingTransport{roundTrip: func(request *http.Request) (*http.Response, error) {
		received[request.URL.Host] = request.Header.Clone()
		return reply(request, http.StatusOK, ""), nil
	}}}
	client, err := httpclient.NewDeliveryClient(httpclient.DeliveryConfig{
		HTTPClient: host, Destination: httpclient.Origins{api, delivery},
		CredentialOrigin: api, CredentialHeaders: []string{"X-Key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if client.Jar != nil || host.Jar == nil {
		t.Fatal("delivery client kept the host's cookie jar or changed the host")
	}
	for _, target := range []string{"https://api.example/file", "https://cdn.example/file"} {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer secret")
		request.Header.Set("X-Key", "secret")
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if request.Header.Get("Authorization") == "" {
			t.Fatal("delivery client modified the caller's request")
		}
	}
	if received["api.example"].Get("Authorization") == "" || received["api.example"].Get("X-Key") == "" {
		t.Fatal("credential origin lost its credentials")
	}
	if received["cdn.example"].Get("Authorization") != "" || received["cdn.example"].Get("X-Key") != "" {
		t.Fatal("delivery origin received credentials")
	}
}

func TestDeliveryClientRefusesDowngradesAndBoundsRedirects(t *testing.T) {
	secure, err := httpclient.ParseOrigin("https://cdn.example")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := httpclient.ParseOrigin("http://cdn.example")
	if err != nil {
		t.Fatal(err)
	}
	for name, location := range map[string]string{"downgrade": "http://cdn.example/next", "loop": "https://cdn.example/next"} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			host := &http.Client{
				CheckRedirect: func(*http.Request, []*http.Request) error { return nil },
				Transport: &recordingTransport{roundTrip: func(request *http.Request) (*http.Response, error) {
					calls++
					return reply(request, http.StatusFound, location), nil
				}},
			}
			client, err := httpclient.NewDeliveryClient(httpclient.DeliveryConfig{HTTPClient: host, Destination: httpclient.Origins{secure, plain}})
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.Get("https://cdn.example/start")
			if response != nil {
				_ = response.Body.Close()
			}
			if err == nil {
				t.Fatal("delivery client followed an unbounded or downgrading redirect")
			}
			if name == "downgrade" && (calls != 1 || !errors.Is(err, httpclient.ErrOriginNotAllowed)) {
				t.Fatalf("calls = %d, error = %v", calls, err)
			}
			if name == "loop" && calls != 10 {
				t.Fatalf("calls = %d; the host's permissive policy lifted the redirect cap", calls)
			}
		})
	}
	if _, err := httpclient.NewDeliveryClient(httpclient.DeliveryConfig{}); err == nil {
		t.Fatal("a delivery client was built without a destination policy")
	}
}
