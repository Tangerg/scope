package httpclient

import (
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/samber/lo"
)

const (
	headerAuthorization      = "Authorization"
	headerProxyAuthorization = "Proxy-Authorization"
	headerCookie             = "Cookie"
)

// Restrict returns a copy of client whose requests, including every redirect,
// reach only allowed origins. A nil client is an empty client on the default
// transport. The host keeps ownership of the shared transport and cookie jar.
func Restrict(client *http.Client, allowed Origins) (*http.Client, error) {
	if len(allowed) == 0 || slices.Contains(allowed, Origin{}) {
		return nil, errors.New("httpclient: restriction requires at least one valid origin")
	}
	owned := copyClient(client)
	restriction{allowed: allowed}.apply(owned)
	return owned, nil
}

// DeliveryConfig restricts a client that follows provider-issued output URLs.
// Such a URL is data, not a grant of the host's network or credentials.
type DeliveryConfig struct {
	HTTPClient *http.Client
	// Destination authorizes every request and every redirect.
	Destination Destination
	// CredentialOrigin is the one origin that may still receive credentials,
	// such as an API that serves its own files; the zero value strips them
	// from every request.
	CredentialOrigin Origin
	// CredentialHeaders names provider credentials removed alongside
	// Authorization, Proxy-Authorization, and Cookie.
	CredentialHeaders []string
}

// NewDeliveryClient returns a copy of the configured client that sends no
// cookie jar, never follows a redirect from HTTPS to HTTP, reaches only the
// Destination, and strips credentials outside CredentialOrigin.
func NewDeliveryClient(config DeliveryConfig) (*http.Client, error) {
	if lo.IsNil(config.Destination) {
		return nil, errors.New("httpclient: delivery requires a destination policy")
	}
	owned := copyClient(config.HTTPClient)
	owned.Jar = nil
	headers := append([]string{headerAuthorization, headerProxyAuthorization, headerCookie}, config.CredentialHeaders...)
	restriction{
		allowed:         config.Destination,
		refuseDowngrade: true,
		credentials:     &credentialPolicy{keep: config.CredentialOrigin, headers: headers},
	}.apply(owned)
	return owned, nil
}

func copyClient(client *http.Client) *http.Client {
	owned := &http.Client{}
	if client != nil {
		*owned = *client
	}
	return owned
}

// restriction composes the guarantees a restricted client adds to the host's
// own: the redirect cap and the host's policy run first, then the destination
// check, so neither the host nor a server can widen the destination.
type restriction struct {
	allowed         Destination
	rejectRedirects bool
	refuseDowngrade bool
	credentials     *credentialPolicy
}

func (r restriction) apply(client *http.Client) {
	transport := client.Transport
	if lo.IsNil(transport) {
		transport = http.DefaultTransport
	}
	client.Transport = &destinationTransport{next: transport, restriction: r}
	previous := client.CheckRedirect
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		// The cap holds even when the host's policy would follow more: a
		// server, not the host, chooses how many redirects to send.
		if len(via) >= maxHTTPRedirects {
			return fmt.Errorf("httpclient: stopped after %d redirects", maxHTTPRedirects)
		}
		if previous != nil {
			if err := previous(request, via); err != nil {
				return err
			}
		}
		if r.refuseDowngrade && len(via) > 0 && via[len(via)-1].URL.Scheme == schemeHTTPS && request.URL.Scheme != schemeHTTPS {
			return fmt.Errorf("%w: redirect downgrades HTTPS", ErrOriginNotAllowed)
		}
		if err := r.allowed.ValidateRequest(request); err != nil {
			return err
		}
		if r.rejectRedirects {
			return http.ErrUseLastResponse
		}
		return nil
	}
}

// The final transport check covers URL changes made by host redirect callbacks.
type destinationTransport struct {
	next        http.RoundTripper
	restriction restriction
}

func (d *destinationTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := d.restriction.allowed.ValidateRequest(request); err != nil {
		if request != nil && request.Body != nil {
			err = errors.Join(err, request.Body.Close())
		}
		return nil, err
	}
	if d.restriction.credentials != nil {
		request = d.restriction.credentials.strip(request)
	}
	return d.next.RoundTrip(request)
}

func (d *destinationTransport) CloseIdleConnections() {
	if closer, ok := d.next.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

type credentialPolicy struct {
	keep    Origin
	headers []string
}

// strip leaves the caller's request untouched: RoundTrip must not modify it.
func (c *credentialPolicy) strip(request *http.Request) *http.Request {
	if c.keep != (Origin{}) && c.keep.ValidateRequest(request) == nil {
		return request
	}
	stripped := request.Clone(request.Context())
	for _, header := range c.headers {
		stripped.Header.Del(header)
	}
	return stripped
}
