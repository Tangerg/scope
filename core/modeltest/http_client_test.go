package modeltest

import (
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/httpclient"
)

func TestHTTPClientContractAcceptsIndependentBoundClients(t *testing.T) {
	previousDefault := http.DefaultTransport
	RunHTTPClientContract(t, HTTPClientContract{
		Build: func(t *testing.T, baseURL string, borrowed *http.Client) []*http.Client {
			clients := make([]*http.Client, 2)
			for index := range clients {
				var err error
				clients[index], err = httpclient.New(t.Context(), httpclient.Config{BaseURL: baseURL, HTTPClient: borrowed})
				if err != nil {
					t.Fatal(err)
				}
			}
			return clients
		},
	})
	if http.DefaultTransport != previousDefault {
		t.Fatal("HTTP contract retained its process-wide transport")
	}
}

func TestHTTPClientObservationRejectsBrokenBorrowing(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*httpClientObservation, []*http.Client) []*http.Client
	}{
		{name: "borrowed transport mutation", mutate: func(observation *httpClientObservation, clients []*http.Client) []*http.Client {
			observation.borrowed.Transport = nil
			return clients
		}},
		{name: "borrowed jar mutation", mutate: func(observation *httpClientObservation, clients []*http.Client) []*http.Client {
			observation.borrowed.Jar = nil
			return clients
		}},
		{name: "borrowed timeout mutation", mutate: func(observation *httpClientObservation, clients []*http.Client) []*http.Client {
			observation.borrowed.Timeout = 0
			return clients
		}},
		{name: "borrowed redirect mutation", mutate: func(observation *httpClientObservation, clients []*http.Client) []*http.Client {
			observation.borrowed.CheckRedirect = nil
			return clients
		}},
		{name: "no clients", mutate: func(*httpClientObservation, []*http.Client) []*http.Client { return nil }},
		{name: "nil client", mutate: func(*httpClientObservation, []*http.Client) []*http.Client { return []*http.Client{nil} }},
		{name: "borrowed client returned", mutate: func(observation *httpClientObservation, _ []*http.Client) []*http.Client {
			return []*http.Client{observation.borrowed}
		}},
		{name: "shared owned configuration", mutate: func(_ *httpClientObservation, clients []*http.Client) []*http.Client {
			return []*http.Client{clients[0], clients[0]}
		}},
		{name: "unbound default transport", mutate: func(_ *httpClientObservation, clients []*http.Client) []*http.Client {
			clients[0].Transport = nil
			return clients
		}},
		{name: "typed nil transport", mutate: func(_ *httpClientObservation, clients []*http.Client) []*http.Client {
			clients[0].Transport = (*httpClientTransport)(nil)
			return clients
		}},
		{name: "lost timeout", mutate: func(_ *httpClientObservation, clients []*http.Client) []*http.Client {
			clients[0].Timeout = 0
			return clients
		}},
		{name: "lost cookie jar", mutate: func(_ *httpClientObservation, clients []*http.Client) []*http.Client {
			clients[0].Jar = nil
			return clients
		}},
		{name: "lost redirect policy", mutate: func(_ *httpClientObservation, clients []*http.Client) []*http.Client {
			clients[0].CheckRedirect = func(*http.Request, []*http.Request) error { return nil }
			return clients
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			redirectErr := errors.New("host policy")
			jar, err := cookiejar.New(nil)
			if err != nil {
				t.Fatal(err)
			}
			borrowed := &http.Client{
				Timeout: httpClientProbeTimeout, Jar: jar, Transport: new(httpClientTransport),
				CheckRedirect: func(*http.Request, []*http.Request) error { return redirectErr },
			}
			owned := *borrowed
			observation := httpClientObservation{
				borrowed: borrowed, configuration: *borrowed,
				defaultTransport: http.DefaultTransport, redirectErr: redirectErr,
			}
			if err := observation.validate(test.mutate(&observation, []*http.Client{&owned})); err == nil {
				t.Fatal("accepted broken HTTP client borrowing")
			}
		})
	}
}

type httpClientRequestBody struct {
	io.Reader
	closed bool
}

func (h *httpClientRequestBody) Close() error { h.closed = true; return nil }

func TestHTTPClientTransportOwnsRequestBody(t *testing.T) {
	body := &httpClientRequestBody{Reader: strings.NewReader("request")}
	request, err := http.NewRequest(http.MethodPost, httpClientProbeURL, body)
	if err != nil {
		t.Fatal(err)
	}
	transport := new(httpClientTransport)
	response, err := transport.RoundTrip(request)
	if err != nil || response.StatusCode != http.StatusOK || !body.closed || transport.calls != 1 {
		t.Fatalf("response=%v error=%v closed=%t requests=%d", response, err, body.closed, transport.calls)
	}
}
