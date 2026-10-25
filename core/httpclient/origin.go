package httpclient

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/samber/lo"
)

const (
	schemeHTTP       = "http"
	schemeHTTPS      = "https"
	portHTTP         = "80"
	portHTTPS        = "443"
	maxHTTPRedirects = 10
)

// ErrOriginNotAllowed prevents an API credential or request body from reaching
// an origin other than the one configured by its owner.
var ErrOriginNotAllowed = errors.New("httpclient: request origin is not authorized")

// Origin is an immutable HTTP origin. Equality includes the scheme, host, and
// effective port; its zero value authorizes no requests.
type Origin struct {
	scheme string
	host   string
}

func NewOrigin(_ context.Context, endpoint *url.URL) (Origin, error) {
	return originFromURL(endpoint)
}

func parseOrigin(raw string) (Origin, error) {
	endpoint, err := url.Parse(raw)
	if err != nil {
		return Origin{}, fmt.Errorf("endpoint must be an absolute HTTP(S) URL: %w", err)
	}
	return originFromURL(endpoint)
}

func originFromURL(endpoint *url.URL) (Origin, error) {
	if endpoint == nil || endpoint.User != nil || endpoint.Hostname() == "" {
		return Origin{}, errors.New("endpoint must have a host and no user information")
	}
	port := endpoint.Port()
	switch endpoint.Scheme {
	case schemeHTTP:
		if port == "" {
			port = portHTTP
		}
	case schemeHTTPS:
		if port == "" {
			port = portHTTPS
		}
	default:
		return Origin{}, errors.New("endpoint scheme must be HTTP or HTTPS")
	}
	return Origin{scheme: endpoint.Scheme, host: net.JoinHostPort(strings.ToLower(endpoint.Hostname()), port)}, nil
}

func (o Origin) validate(endpoint *url.URL) error {
	actual, err := originFromURL(endpoint)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrOriginNotAllowed, err)
	}
	if actual != o {
		return fmt.Errorf("%w: %s://%s", ErrOriginNotAllowed, actual.scheme, actual.host)
	}
	return nil
}

// ValidateRequest checks both the destination URL and the authority sent on the
// wire, because Request.Host can override URL.Host without changing the URL.
func (o Origin) ValidateRequest(request *http.Request) error {
	if request == nil {
		return fmt.Errorf("%w: request is nil", ErrOriginNotAllowed)
	}
	if err := o.validate(request.URL); err != nil {
		return err
	}
	if request.Host != "" {
		return o.validate(&url.URL{Scheme: request.URL.Scheme, Host: request.Host})
	}
	return nil
}

func (o Origin) restrict(client *http.Client) {
	transport := client.Transport
	if lo.IsNil(transport) {
		transport = http.DefaultTransport
	}
	client.Transport = &originTransport{next: transport, origin: o}
	previous := client.CheckRedirect
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if previous != nil {
			if err := previous(request, via); err != nil {
				return err
			}
		} else if len(via) >= maxHTTPRedirects {
			return fmt.Errorf("httpclient: stopped after %d redirects", maxHTTPRedirects)
		}
		return o.ValidateRequest(request)
	}
}

// The final transport check covers URL changes made by host redirect callbacks.
type originTransport struct {
	next   http.RoundTripper
	origin Origin
}

func (o *originTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := o.origin.ValidateRequest(request); err != nil {
		if request != nil && request.Body != nil {
			err = errors.Join(err, request.Body.Close())
		}
		return nil, err
	}
	return o.next.RoundTrip(request)
}

func (o *originTransport) CloseIdleConnections() {
	if closer, ok := o.next.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}
