package mistral

import (
	"errors"
	"net/http"
	"net/http/cookiejar"
	"testing"
	"time"
)

type borrowedTransport struct{ calls int }

func (b *borrowedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	b.calls++
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody, Request: request}, nil
}

func TestConstructionBorrowsHTTPConfiguration(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "default transport", true: "explicit transport"}[explicit], func(t *testing.T) {
			router := new(borrowedTransport)
			previousDefault := http.DefaultTransport
			http.DefaultTransport = router
			defer func() { http.DefaultTransport = previousDefault }()
			jar, err := cookiejar.New(nil)
			if err != nil {
				t.Fatal(err)
			}
			redirectErr := errors.New("host redirect policy")
			host := &http.Client{Timeout: time.Second, Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return redirectErr }}
			if explicit {
				host.Transport = router
			}
			config := apiConfig{APIKey: "test", BaseURL: "https://provider.example.test", HTTPClient: host}
			adapter, err := newAPI(config)
			if err != nil {
				t.Fatal(err)
			}
			owned := adapter.http.GetClient()
			if owned == host || owned.Transport != router || host.Transport != nil && !explicit || host.Jar != jar || host.Timeout != time.Second || http.DefaultTransport != router {
				t.Fatal("constructor changed or failed to preserve borrowed HTTP configuration")
			}
			redirectRequest, err := http.NewRequest(http.MethodGet, config.BaseURL, nil)
			if err != nil {
				t.Fatal(err)
			}
			if owned.Timeout != host.Timeout || owned.Jar != jar || !errors.Is(owned.CheckRedirect(redirectRequest, nil), redirectErr) || !errors.Is(host.CheckRedirect(redirectRequest, nil), redirectErr) {
				t.Fatal("host policy lost")
			}
			for _, client := range []*http.Client{host, owned} {
				response, err := client.Get("scope-test://routing/")
				if err != nil {
					t.Fatal(err)
				}
				response.Body.Close()
			}
			if router.calls != 2 {
				t.Fatalf("transport calls=%d", router.calls)
			}
		})
	}
}
