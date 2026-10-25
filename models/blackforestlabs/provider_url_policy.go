package blackforestlabs

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/Tangerg/scope/core/httpclient"
)

const (
	providerSchemeHTTPS = "https"
	providerDomain      = "bfl.ai"
)

// Issued polling and delivery URLs may use different BFL regions. The provider
// namespace admits those URLs; each request still has one exact authority.
type providerURLPolicy struct {
	configuredOrigin httpclient.Origin
}

func (p providerURLPolicy) parse(ctx context.Context, raw string) (*url.URL, error) {
	endpoint, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if _, admissionErr := p.origin(ctx, endpoint); admissionErr != nil {
		return nil, admissionErr
	}
	return endpoint, nil
}

func (p providerURLPolicy) origin(ctx context.Context, endpoint *url.URL) (httpclient.Origin, error) {
	origin, err := httpclient.NewOrigin(ctx, endpoint)
	if err != nil {
		return httpclient.Origin{}, err
	}
	if origin == p.configuredOrigin {
		return origin, nil
	}
	hostname := strings.ToLower(endpoint.Hostname())
	if endpoint.Scheme != providerSchemeHTTPS || (hostname != providerDomain && !strings.HasSuffix(hostname, "."+providerDomain)) {
		return httpclient.Origin{}, fmt.Errorf("%w: blackforestlabs: URL is outside the configured origin or official provider namespace: %s", httpclient.ErrOriginNotAllowed, endpoint)
	}
	return origin, nil
}

func (p providerURLPolicy) validateRequest(request *http.Request) error {
	origin, err := p.origin(request.Context(), request.URL)
	if err != nil {
		return err
	}
	return origin.ValidateRequest(request)
}
