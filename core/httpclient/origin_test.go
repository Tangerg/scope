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
			left, err := httpclient.NewOrigin(t.Context(), leftURL)
			if err != nil {
				t.Fatal(err)
			}
			right, err := httpclient.NewOrigin(t.Context(), rightURL)
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
	origin, err := httpclient.NewOrigin(t.Context(), endpoint)
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
	if _, err = httpclient.NewOrigin(t.Context(), nil); err == nil {
		t.Fatal("nil endpoint constructed an origin")
	}
}
