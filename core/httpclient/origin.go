package httpclient

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
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

// Origin is an immutable HTTP origin. Only hierarchical URLs are admitted, so
// Opaque cannot replace the checked authority on the wire. Equality includes
// the scheme, host, and effective port; its zero value authorizes no requests.
type Origin struct {
	scheme string
	host   string
}

// NewOrigin takes the origin of an absolute, hierarchical HTTP(S) URL. Its
// path, query, and fragment name a resource and do not affect the origin.
func NewOrigin(endpoint *url.URL) (Origin, error) {
	return originFromURL(endpoint)
}

// ParseOrigin admits a configured origin: an absolute HTTP(S) URL with no path
// beyond "/", query, or fragment, so a value that names a resource cannot be
// mistaken for the authority it grants.
func ParseOrigin(raw string) (Origin, error) {
	endpoint, err := url.Parse(raw)
	if err != nil {
		return Origin{}, fmt.Errorf("origin must be an absolute HTTP(S) URL: %w", err)
	}
	if (endpoint.Path != "" && endpoint.Path != "/") || endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" {
		return Origin{}, errors.New("origin must contain only a scheme, host, and port")
	}
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
	if endpoint == nil || endpoint.Opaque != "" || endpoint.User != nil || endpoint.Hostname() == "" {
		return Origin{}, errors.New("endpoint must be hierarchical, have a host, and have no user information")
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

// Origins is a set of authorized origins. A request is authorized when its
// destination, including the authority written to the wire, is one of them.
type Origins []Origin

func (o Origins) ValidateRequest(request *http.Request) error {
	if request == nil {
		return fmt.Errorf("%w: request is nil", ErrOriginNotAllowed)
	}
	destination, err := originFromURL(request.URL)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrOriginNotAllowed, err)
	}
	if !slices.Contains(o, destination) {
		return fmt.Errorf("%w: %s://%s", ErrOriginNotAllowed, destination.scheme, destination.host)
	}
	return destination.ValidateRequest(request)
}

// Restrict returns a copy of client whose requests, including every redirect,
// reach only allowed origins. A nil client is an empty client on the default
// transport. The host keeps ownership of the shared transport and cookie jar.
func Restrict(client *http.Client, allowed Origins) (*http.Client, error) {
	if len(allowed) == 0 || slices.Contains(allowed, Origin{}) {
		return nil, errors.New("httpclient: restriction requires at least one valid origin")
	}
	owned := http.Client{}
	if client != nil {
		owned = *client
	}
	allowed.restrict(&owned, false)
	return &owned, nil
}

// restrict chains the host's redirect policy, then the origin check, then the
// optional refusal to follow, so no setting replaces another.
func (o Origins) restrict(client *http.Client, rejectRedirects bool) {
	transport := client.Transport
	if lo.IsNil(transport) {
		transport = http.DefaultTransport
	}
	client.Transport = &originTransport{next: transport, allowed: o}
	previous := client.CheckRedirect
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if previous != nil {
			if err := previous(request, via); err != nil {
				return err
			}
		} else if len(via) >= maxHTTPRedirects {
			return fmt.Errorf("httpclient: stopped after %d redirects", maxHTTPRedirects)
		}
		if err := o.ValidateRequest(request); err != nil {
			return err
		}
		if rejectRedirects {
			return http.ErrUseLastResponse
		}
		return nil
	}
}

// The final transport check covers URL changes made by host redirect callbacks.
type originTransport struct {
	next    http.RoundTripper
	allowed Origins
}

func (o *originTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := o.allowed.ValidateRequest(request); err != nil {
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
