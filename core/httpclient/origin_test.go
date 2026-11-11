package httpclient_test

import (
	"errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/Tangerg/scope/core/httpclient"
)

func TestOriginEqualityUsesEffectivePortAndIgnoresResource(t *testing.T) {
	for _, test := range []struct {
		left, right string
		equal       bool
	}{
		{left: "https://API.example/resource?q=one", right: "https://api.example:443/other#fragment", equal: true},
		{left: "http://API.example", right: "http://api.example:80", equal: true},
		{left: "https://[::1]", right: "https://[::1]:443", equal: true},
		{left: "https://api.example", right: "https://api.example:8443"},
		{left: "https://api.example", right: "http://api.example:443"},
		{left: "https://api.example", right: "https://child.api.example"},
	} {
		t.Run(test.left+"/"+test.right, func(t *testing.T) {
			leftURL, err := url.Parse(test.left)
			if err != nil {
				t.Fatal(err)
			}
			rightURL, err := url.Parse(test.right)
			if err != nil {
				t.Fatal(err)
			}
			left, err := httpclient.NewOrigin(leftURL)
			if err != nil {
				t.Fatal(err)
			}
			right, err := httpclient.NewOrigin(rightURL)
			if err != nil {
				t.Fatal(err)
			}
			if (left == right) != test.equal {
				t.Fatalf("origin equality = %t, want %t", left == right, test.equal)
			}
		})
	}
}

func TestOriginRejectsMissingAndConflictingAuthorities(t *testing.T) {
	endpoint, err := url.Parse("https://api.example")
	if err != nil {
		t.Fatal(err)
	}
	origin, err := httpclient.NewOrigin(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	endpoint.Host = "changed.example"
	for _, request := range []*http.Request{
		nil,
		{},
		{URL: &url.URL{Scheme: "https", Host: "other.example"}},
		{URL: &url.URL{Scheme: "https", Host: "api.example"}, Host: "other.example"},
		{URL: &url.URL{Scheme: "https", Host: "api.example"}, Host: "api.example:8443"},
	} {
		if validateErr := origin.ValidateRequest(request); !errors.Is(validateErr, httpclient.ErrOriginNotAllowed) {
			t.Fatalf("unauthorized request: %v", validateErr)
		}
	}
	request := &http.Request{URL: &url.URL{Scheme: "https", Host: "API.example:443"}, Host: "api.example"}
	if err = origin.ValidateRequest(request); err != nil {
		t.Fatalf("immutable origin lost equivalent authority: %v", err)
	}
	if err = (httpclient.Origin{}).ValidateRequest(request); !errors.Is(err, httpclient.ErrOriginNotAllowed) {
		t.Fatalf("zero origin authorized a request: %v", err)
	}
	if _, err = httpclient.NewOrigin(nil); err == nil {
		t.Fatal("nil endpoint constructed an origin")
	}
}

func TestOriginRejectsOpaqueEndpoints(t *testing.T) {
	endpoint := &url.URL{Scheme: "http", Host: "api.example", Opaque: "//other.example/private"}
	if _, err := httpclient.NewOrigin(endpoint); err == nil {
		t.Fatal("opaque endpoint constructed an origin from an authority it can replace")
	}
}

func TestParseOriginAdmitsOnlyAnAuthority(t *testing.T) {
	for raw, valid := range map[string]bool{
		"https://api.example":      true,
		"https://api.example/":     true,
		"http://api.example:8080":  true,
		"https://api.example/v1":   false,
		"https://api.example/?q=1": false,
		"https://api.example/?":    false,
		"https://api.example/#top": false,
		"ftp://api.example":        false,
		"https://user@api.example": false,
		"/relative":                false,
	} {
		if _, err := httpclient.ParseOrigin(raw); (err == nil) != valid {
			t.Errorf("ParseOrigin(%q) error = %v, want valid %t", raw, err, valid)
		}
	}
}

func TestRestrictedClientReachesOnlyItsOrigins(t *testing.T) {
	first, err := httpclient.ParseOrigin("https://first.example")
	if err != nil {
		t.Fatal(err)
	}
	second, err := httpclient.ParseOrigin("https://second.example")
	if err != nil {
		t.Fatal(err)
	}
	if _, restrictErr := httpclient.Restrict(nil, nil); restrictErr == nil {
		t.Fatal("a client was restricted to no origin")
	}
	if _, restrictErr := httpclient.Restrict(nil, httpclient.Origins{first, {}}); restrictErr == nil {
		t.Fatal("a client was restricted to the zero origin")
	}
	calls := 0
	host := &http.Client{Transport: &recordingTransport{roundTrip: func(request *http.Request) (*http.Response, error) {
		calls++
		return reply(request, http.StatusOK, ""), nil
	}}}
	restricted, err := httpclient.Restrict(host, httpclient.Origins{first, second})
	if err != nil {
		t.Fatal(err)
	}
	for target, allowed := range map[string]bool{
		"https://first.example/a":  true,
		"https://second.example/b": true,
		"https://third.example/c":  false,
	} {
		response, err := restricted.Get(target)
		if allowed != (err == nil) || err != nil && !errors.Is(err, httpclient.ErrOriginNotAllowed) {
			t.Fatalf("Get(%s) error = %v, want allowed %t", target, err, allowed)
		}
		if response != nil {
			_ = response.Body.Close()
		}
	}
	if calls != 2 || restricted == host || host.CheckRedirect != nil {
		t.Fatalf("calls = %d; restriction must reach only authorized origins without changing the host", calls)
	}
}
