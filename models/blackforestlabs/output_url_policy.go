package blackforestlabs

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/httpclient"
)

const (
	defaultOutputOrigin      = "https://bfldeliveryprod.blob.core.windows.net"
	maximumDownloadRedirects = 10
	headerAuthorization      = "Authorization"
	headerProxyAuthorization = "Proxy-Authorization"
	headerCookie             = "Cookie"
)

// API authority does not grant delivery authority. Presigned assets use
// credential-free requests to official delivery origins or host-authorized ones.
type outputURLPolicy struct {
	origins []httpclient.Origin
}

func newOutputURLPolicy(ctx context.Context, configured []string) (outputURLPolicy, error) {
	policy := outputURLPolicy{origins: make([]httpclient.Origin, 0, len(configured)+1)}
	for index, raw := range append([]string{defaultOutputOrigin}, configured...) {
		endpoint, err := url.Parse(raw)
		if err != nil {
			return outputURLPolicy{}, fmt.Errorf("blackforestlabs: output origin %d: %w", index, err)
		}
		if (endpoint.Path != "" && endpoint.Path != "/") || endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" {
			return outputURLPolicy{}, fmt.Errorf("blackforestlabs: output origin %d must contain only an HTTP origin", index)
		}
		origin, err := httpclient.NewOrigin(ctx, endpoint)
		if err != nil {
			return outputURLPolicy{}, fmt.Errorf("blackforestlabs: output origin %d: %w", index, err)
		}
		if !slices.Contains(policy.origins, origin) {
			policy.origins = append(policy.origins, origin)
		}
	}
	return policy, nil
}

func (o outputURLPolicy) origin(ctx context.Context, endpoint *url.URL) (httpclient.Origin, error) {
	origin, err := httpclient.NewOrigin(ctx, endpoint)
	if err != nil {
		return httpclient.Origin{}, err
	}
	if slices.Contains(o.origins, origin) || isRegionalProviderURL(endpoint) {
		return origin, nil
	}
	return httpclient.Origin{}, fmt.Errorf("blackforestlabs: output origin is not authorized: %w", httpclient.ErrOriginNotAllowed)
}

func (o outputURLPolicy) parse(ctx context.Context, raw string) (*url.URL, error) {
	endpoint, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if _, err := o.origin(ctx, endpoint); err != nil {
		return nil, err
	}
	return endpoint, nil
}

func (o outputURLPolicy) validateRequest(request *http.Request) error {
	origin, err := o.origin(request.Context(), request.URL)
	if err != nil {
		return err
	}
	return origin.ValidateRequest(request)
}

func (o outputURLPolicy) client(source *http.Client) *http.Client {
	client := *source
	client.Jar = nil
	transport := client.Transport
	if lo.IsNil(transport) {
		transport = http.DefaultTransport
	}
	client.Transport = &downloadTransport{next: transport, urls: o}
	previous := client.CheckRedirect
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= maximumDownloadRedirects {
			return fmt.Errorf("blackforestlabs: stopped after %d output redirects", maximumDownloadRedirects)
		}
		if previous != nil {
			if err := previous(request, via); err != nil {
				return err
			}
		}
		if len(via) > 0 && via[len(via)-1].URL.Scheme == providerSchemeHTTPS && request.URL.Scheme != providerSchemeHTTPS {
			return fmt.Errorf("blackforestlabs: output redirect downgrades HTTPS: %w", httpclient.ErrOriginNotAllowed)
		}
		return o.validateRequest(request)
	}
	return &client
}
