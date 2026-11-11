package blackforestlabs

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"

	"github.com/Tangerg/scope/core/httpclient"
)

const defaultOutputOrigin = "https://bfldeliveryprod.blob.core.windows.net"

// API authority does not grant delivery authority. Presigned assets use
// credential-free requests to official delivery origins or host-authorized ones.
type outputURLPolicy struct {
	origins []httpclient.Origin
}

func newOutputURLPolicy(configured []string) (outputURLPolicy, error) {
	policy := outputURLPolicy{origins: make([]httpclient.Origin, 0, len(configured)+1)}
	for index, raw := range append([]string{defaultOutputOrigin}, configured...) {
		origin, err := httpclient.ParseOrigin(raw)
		if err != nil {
			return outputURLPolicy{}, fmt.Errorf("blackforestlabs: output origin %d: %w", index, err)
		}
		if !slices.Contains(policy.origins, origin) {
			policy.origins = append(policy.origins, origin)
		}
	}
	return policy, nil
}

func (o outputURLPolicy) origin(endpoint *url.URL) (httpclient.Origin, error) {
	origin, err := httpclient.NewOrigin(endpoint)
	if err != nil {
		return httpclient.Origin{}, err
	}
	if slices.Contains(o.origins, origin) || isRegionalProviderURL(endpoint) {
		return origin, nil
	}
	return httpclient.Origin{}, fmt.Errorf("blackforestlabs: output origin is not authorized: %w", httpclient.ErrOriginNotAllowed)
}

func (o outputURLPolicy) parse(raw string) (*url.URL, error) {
	endpoint, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if _, err := o.origin(endpoint); err != nil {
		return nil, err
	}
	return endpoint, nil
}

// ValidateRequest makes the policy the delivery client's destination: an
// official or host-authorized origin, or a regional provider URL.
func (o outputURLPolicy) ValidateRequest(request *http.Request) error {
	if request == nil {
		return fmt.Errorf("blackforestlabs: nil output request: %w", httpclient.ErrOriginNotAllowed)
	}
	origin, err := o.origin(request.URL)
	if err != nil {
		return err
	}
	return origin.ValidateRequest(request)
}

// client never sends credentials, including the API key, to a delivery URL.
func (o outputURLPolicy) client(source *http.Client) (*http.Client, error) {
	return httpclient.NewDeliveryClient(httpclient.DeliveryConfig{HTTPClient: source, Destination: o, CredentialHeaders: []string{headerAPIKey}})
}
