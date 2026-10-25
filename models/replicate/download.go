package replicate

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/httpclient"
)

type outputPolicy struct {
	apiOrigin httpclient.Origin
}

func (o outputPolicy) originFor(ctx context.Context, endpoint *url.URL) (httpclient.Origin, error) {
	origin, err := httpclient.NewOrigin(ctx, endpoint)
	if err != nil {
		return httpclient.Origin{}, fmt.Errorf("replicate: invalid output URL: %w: %w", httpclient.ErrOriginNotAllowed, err)
	}
	if origin == o.apiOrigin {
		return origin, nil
	}
	host := strings.ToLower(endpoint.Hostname())
	if host != deliveryHost && !strings.HasSuffix(host, deliverySubdomainSuffix) {
		return httpclient.Origin{}, fmt.Errorf("replicate: untrusted output host %q: %w", host, httpclient.ErrOriginNotAllowed)
	}
	delivery, err := httpclient.NewOrigin(ctx, &url.URL{Scheme: schemeHTTPS, Host: host})
	if err != nil {
		return httpclient.Origin{}, fmt.Errorf("replicate: invalid delivery origin: %w", err)
	}
	if origin != delivery {
		return httpclient.Origin{}, fmt.Errorf("replicate: output URL must use HTTPS on the standard port: %w", httpclient.ErrOriginNotAllowed)
	}
	return origin, nil
}

func (o outputPolicy) authorizeRequest(request *http.Request) (httpclient.Origin, error) {
	if request == nil {
		return httpclient.Origin{}, fmt.Errorf("replicate: nil output request: %w", httpclient.ErrOriginNotAllowed)
	}
	origin, err := o.originFor(request.Context(), request.URL)
	if err != nil {
		return httpclient.Origin{}, err
	}
	if err = origin.ValidateRequest(request); err != nil {
		return httpclient.Origin{}, err
	}
	return origin, nil
}

func (o outputPolicy) client(source *http.Client) *http.Client {
	owned := &http.Client{}
	if source != nil {
		*owned = *source
	}
	transport := owned.Transport
	if lo.IsNil(transport) {
		transport = http.DefaultTransport
	}
	owned.Transport = &outputTransport{next: transport, policy: o}
	previous := owned.CheckRedirect
	owned.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= maximumDownloadRedirects {
			return fmt.Errorf("replicate: stopped after %d redirects", maximumDownloadRedirects)
		}
		if previous != nil {
			if err := previous(request, via); err != nil {
				return err
			}
		}
		return o.authorizeRedirect(request, via)
	}
	return owned
}

func (o outputPolicy) authorizeRedirect(request *http.Request, via []*http.Request) error {
	origin, err := o.authorizeRequest(request)
	if err != nil || len(via) == 0 {
		return err
	}
	previous := via[len(via)-1].URL
	if previous.Scheme == schemeHTTPS && request.URL.Scheme != schemeHTTPS {
		return fmt.Errorf("replicate: HTTPS redirect downgrade is forbidden: %w", httpclient.ErrOriginNotAllowed)
	}
	previousOrigin, err := httpclient.NewOrigin(request.Context(), previous)
	if err != nil {
		return fmt.Errorf("replicate: invalid previous redirect origin: %w", err)
	}
	if origin != previousOrigin {
		removeOutputCredentials(request.Header)
	}
	return nil
}

// Redirect callbacks are not the final sender; the transport also checks the
// actual URL and virtual authority before sharing the host's connection pool.
type outputTransport struct {
	next   http.RoundTripper
	policy outputPolicy
}

func (o *outputTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	origin, err := o.policy.authorizeRequest(request)
	if err != nil {
		if request != nil && request.Body != nil {
			err = errors.Join(err, request.Body.Close())
		}
		return nil, err
	}
	if origin != o.policy.apiOrigin {
		request = request.Clone(request.Context())
		removeOutputCredentials(request.Header)
	}
	return o.next.RoundTrip(request)
}

func (o *outputTransport) CloseIdleConnections() {
	if closer, ok := o.next.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func removeOutputCredentials(header http.Header) {
	header.Del(headerAuthorization)
	header.Del(headerProxyAuthorization)
	header.Del(headerCookie)
}
