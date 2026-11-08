package blackforestlabs

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/Tangerg/scope/core/httpclient"
)

const (
	providerSchemeHTTPS = "https"
	providerDomain      = "bfl.ai"
	providerHTTPSPort   = "443"
)

// Regional polling may carry API credentials only within the provider's
// namespace or the explicitly configured API origin.
type pollingURLPolicy struct {
	configuredOrigin httpclient.Origin
}

func (p pollingURLPolicy) parse(ctx context.Context, raw string) (*url.URL, error) {
	endpoint, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if _, admissionErr := p.origin(ctx, endpoint); admissionErr != nil {
		return nil, admissionErr
	}
	return endpoint, nil
}

func (p pollingURLPolicy) origin(ctx context.Context, endpoint *url.URL) (httpclient.Origin, error) {
	origin, err := httpclient.NewOrigin(ctx, endpoint)
	if err != nil {
		return httpclient.Origin{}, err
	}
	if origin == p.configuredOrigin {
		return origin, nil
	}
	if !isRegionalProviderURL(endpoint) {
		return httpclient.Origin{}, fmt.Errorf("%w: blackforestlabs: URL is outside the configured origin or official provider namespace: %s", httpclient.ErrOriginNotAllowed, endpoint)
	}
	return origin, nil
}

func isRegionalProviderURL(endpoint *url.URL) bool {
	hostname := strings.ToLower(endpoint.Hostname())
	return endpoint.Scheme == providerSchemeHTTPS &&
		(endpoint.Port() == "" || endpoint.Port() == providerHTTPSPort) &&
		(hostname == providerDomain || strings.HasSuffix(hostname, "."+providerDomain))
}
