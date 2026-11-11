package luma

import (
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/httpclient"
)

const (
	maximumOutputRedirects   = 10
	outputSchemeHTTPS        = "https"
	headerAuthorization      = "Authorization"
	headerProxyAuthorization = "Proxy-Authorization"
	headerCookie             = "Cookie"
)

// Provider-generated URLs are data, not a grant to use the host's network or
// cookies. Only the host can authorize output origins for this binding.
type outputPolicy struct {
	origins []httpclient.Origin
}

func newOutputPolicy(origins []string) (outputPolicy, error) {
	if len(origins) == 0 {
		return outputPolicy{}, errors.New("luma: OutputOrigins must authorize at least one output origin")
	}
	policy := outputPolicy{origins: make(httpclient.Origins, 0, len(origins))}
	for index, raw := range origins {
		origin, err := httpclient.ParseOrigin(raw)
		if err != nil {
			return outputPolicy{}, fmt.Errorf("luma: OutputOrigins[%d]: %w", index, err)
		}
		if !slices.Contains(policy.origins, origin) {
			policy.origins = append(policy.origins, origin)
		}
	}
	return policy, nil
}

func (o outputPolicy) authorize(request *http.Request) error {
	if request != nil {
		for _, origin := range o.origins {
			if origin.ValidateRequest(request) == nil {
				return nil
			}
		}
	}
	return fmt.Errorf("luma: output destination is not authorized: %w", httpclient.ErrOriginNotAllowed)
}

func (o outputPolicy) client(source *http.Client) *http.Client {
	owned := &http.Client{}
	if source != nil {
		*owned = *source
	}
	owned.Jar = nil
	transport := owned.Transport
	if lo.IsNil(transport) {
		transport = http.DefaultTransport
	}
	owned.Transport = &outputTransport{next: transport, policy: o}
	previous := owned.CheckRedirect
	owned.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= maximumOutputRedirects {
			return fmt.Errorf("luma: stopped after %d output redirects", maximumOutputRedirects)
		}
		if previous != nil {
			if err := previous(request, via); err != nil {
				return err
			}
		}
		if len(via) > 0 && via[len(via)-1].URL.Scheme == outputSchemeHTTPS && request.URL.Scheme != outputSchemeHTTPS {
			return fmt.Errorf("luma: output redirect downgrades HTTPS: %w", httpclient.ErrOriginNotAllowed)
		}
		return o.authorize(request)
	}
	return owned
}

// The transport checks the final URL and virtual authority after host redirect
// callbacks, then removes credentials even when the output shares the API host.
type outputTransport struct {
	next   http.RoundTripper
	policy outputPolicy
}

func (o *outputTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := o.policy.authorize(request); err != nil {
		if request != nil && request.Body != nil {
			err = errors.Join(err, request.Body.Close())
		}
		return nil, err
	}
	request = request.Clone(request.Context())
	request.Header.Del(headerAuthorization)
	request.Header.Del(headerProxyAuthorization)
	request.Header.Del(headerCookie)
	return o.next.RoundTrip(request)
}

func (o *outputTransport) CloseIdleConnections() {
	if closer, ok := o.next.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}
