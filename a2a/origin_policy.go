package a2a

import (
	"fmt"
	"net/url"
	"slices"

	sdka2a "github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/Tangerg/scope/core/httpclient"
)

type endpointOriginPolicy struct {
	cardOrigins httpclient.Origins
	rpcOrigins  httpclient.Origins
}

func newEndpointOriginPolicy(cardURL string, allowedRPCOrigins []string) (endpointOriginPolicy, error) {
	cardOrigin, err := originFromURLString(cardURL)
	if err != nil {
		return endpointOriginPolicy{}, fmt.Errorf("%w: %w", ErrInvalidCardURL, err)
	}
	policy := endpointOriginPolicy{
		cardOrigins: httpclient.Origins{cardOrigin},
		rpcOrigins:  httpclient.Origins{cardOrigin},
	}
	for _, rawOrigin := range allowedRPCOrigins {
		origin, err := httpclient.ParseOrigin(rawOrigin)
		if err != nil {
			return endpointOriginPolicy{}, fmt.Errorf("%w: %w", ErrInvalidRPCOrigin, err)
		}
		if !slices.Contains(policy.rpcOrigins, origin) {
			policy.rpcOrigins = append(policy.rpcOrigins, origin)
		}
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
		if !slices.Contains(e.rpcOrigins, origin) {
			return fmt.Errorf("%w: supported interface %d", httpclient.ErrOriginNotAllowed, i)
		}
	}
	return nil
}

func originFromURLString(rawURL string) (httpclient.Origin, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return httpclient.Origin{}, fmt.Errorf("parse URL: %w", err)
	}
	return httpclient.NewOrigin(u)
}
