package replicate

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/Tangerg/scope/core/httpclient"
)

type outputPolicy struct {
	apiOrigin httpclient.Origin
}

func (o outputPolicy) originFor(endpoint *url.URL) (httpclient.Origin, error) {
	origin, err := httpclient.NewOrigin(endpoint)
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
	delivery, err := httpclient.NewOrigin(&url.URL{Scheme: schemeHTTPS, Host: host})
	if err != nil {
		return httpclient.Origin{}, fmt.Errorf("replicate: invalid delivery origin: %w", err)
	}
	if origin != delivery {
		return httpclient.Origin{}, fmt.Errorf("replicate: output URL must use HTTPS on the standard port: %w", httpclient.ErrOriginNotAllowed)
	}
	return origin, nil
}

// ValidateRequest makes the policy the delivery client's destination: the API
// origin, or an HTTPS Replicate delivery host.
func (o outputPolicy) ValidateRequest(request *http.Request) error {
	if request == nil {
		return fmt.Errorf("replicate: nil output request: %w", httpclient.ErrOriginNotAllowed)
	}
	origin, err := o.originFor(request.URL)
	if err != nil {
		return err
	}
	return origin.ValidateRequest(request)
}
