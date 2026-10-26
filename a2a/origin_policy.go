package a2a

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	sdka2a "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/httpclient"
)

const maxHTTPRedirects = 10

type originSet map[httpclient.Origin]struct{}

func (o originSet) contains(origin httpclient.Origin) bool {
	_, ok := o[origin]
	return ok
}

func (o originSet) validateRequest(request *http.Request) error {
	if request == nil {
		return fmt.Errorf("%w: request is nil", ErrOriginNotAllowed)
	}
	origin, err := httpclient.NewOrigin(request.Context(), request.URL)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrOriginNotAllowed, err)
	}
	if !o.contains(origin) {
		return fmt.Errorf("%w: %s://%s", ErrOriginNotAllowed, request.URL.Scheme, request.URL.Host)
	}
	if err := origin.ValidateRequest(request); err != nil {
		return fmt.Errorf("%w: %w", ErrOriginNotAllowed, err)
	}
	return nil
}

func (o originSet) restrict(base *http.Client) *http.Client {
	client := *base
	transport := client.Transport
	if lo.IsNil(transport) {
		transport = http.DefaultTransport
	}
	client.Transport = &originRoundTripper{base: transport, allowed: o}
	previousRedirectPolicy := client.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if previousRedirectPolicy != nil {
			if err := previousRedirectPolicy(req, via); err != nil {
				return err
			}
		} else if len(via) >= maxHTTPRedirects {
			return fmt.Errorf("a2a: stopped after %d redirects", maxHTTPRedirects)
		}
		return o.validateRequest(req)
	}
	return &client
}

type endpointOriginPolicy struct {
	cardOrigins originSet
	rpcOrigins  originSet
}

func newEndpointOriginPolicy(cardURL string, allowedRPCOrigins []string) (endpointOriginPolicy, error) {
	cardOrigin, err := originFromURLString(cardURL)
	if err != nil {
		return endpointOriginPolicy{}, fmt.Errorf("%w: %w", ErrInvalidCardURL, err)
	}
	policy := endpointOriginPolicy{
		cardOrigins: originSet{cardOrigin: {}},
		rpcOrigins:  originSet{cardOrigin: {}},
	}
	for _, rawOrigin := range allowedRPCOrigins {
		origin, err := parseConfiguredOrigin(rawOrigin)
		if err != nil {
			return endpointOriginPolicy{}, fmt.Errorf("%w: %w", ErrInvalidRPCOrigin, err)
		}
		policy.rpcOrigins[origin] = struct{}{}
	}
	return policy, nil
}

func (e endpointOriginPolicy) validateCard(card *sdka2a.AgentCard) error {
	for i, iface := range card.SupportedInterfaces {
		if iface == nil {
			return fmt.Errorf("%w: supported interface %d is nil", ErrInvalidCard, i)
		}
		switch iface.ProtocolBinding {
		case sdka2a.TransportProtocolJSONRPC, sdka2a.TransportProtocolHTTPJSON:
		default:
			continue
		}
		origin, err := originFromURLString(iface.URL)
		if err != nil {
			return fmt.Errorf("%w: supported interface %d URL: %w", ErrInvalidCard, i, err)
		}
		if !e.rpcOrigins.contains(origin) {
			return fmt.Errorf("%w: supported interface %d", ErrOriginNotAllowed, i)
		}
	}
	return nil
}

func originFromURLString(rawURL string) (httpclient.Origin, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return httpclient.Origin{}, fmt.Errorf("parse URL: %w", err)
	}
	return httpclient.NewOrigin(context.Background(), u)
}

func parseConfiguredOrigin(rawOrigin string) (httpclient.Origin, error) {
	u, err := url.Parse(rawOrigin)
	if err != nil {
		return httpclient.Origin{}, fmt.Errorf("parse URL: %w", err)
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return httpclient.Origin{}, errors.New("must contain only scheme and host")
	}
	return httpclient.NewOrigin(context.Background(), u)
}

type originRoundTripper struct {
	base    http.RoundTripper
	allowed originSet
}

func (o *originRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := o.allowed.validateRequest(req); err != nil {
		if req != nil && req.Body != nil {
			err = errors.Join(err, req.Body.Close())
		}
		return nil, err
	}
	return o.base.RoundTrip(req)
}

func (o *originRoundTripper) CloseIdleConnections() {
	if closer, ok := o.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}
