package httpclient

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
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
	return NewOrigin(endpoint)
}

// NewOrigin takes the origin of an absolute, hierarchical HTTP(S) URL. Its
// path, query, and fragment name a resource and do not affect the origin.
func NewOrigin(endpoint *url.URL) (Origin, error) {
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
	actual, err := NewOrigin(endpoint)
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

// Destination authorizes the target of one request, including the authority
// written to the wire. Origins is the fixed-set Destination; a provider whose
// output hosts follow a rule supplies its own.
type Destination interface {
	ValidateRequest(request *http.Request) error
}

// Origins is a set of authorized origins. A request is authorized when its
// destination, including the authority written to the wire, is one of them.
type Origins []Origin

func (o Origins) ValidateRequest(request *http.Request) error {
	if request == nil {
		return fmt.Errorf("%w: request is nil", ErrOriginNotAllowed)
	}
	destination, err := NewOrigin(request.URL)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrOriginNotAllowed, err)
	}
	if !slices.Contains(o, destination) {
		return fmt.Errorf("%w: %s://%s", ErrOriginNotAllowed, destination.scheme, destination.host)
	}
	return destination.ValidateRequest(request)
}
