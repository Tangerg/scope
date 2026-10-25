package blackforestlabs

import (
	"errors"
	"net/http"
)

type downloadTransport struct {
	next http.RoundTripper
	urls providerURLPolicy
}

func (d *downloadTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := d.urls.validateRequest(request); err != nil {
		if request.Body != nil {
			err = errors.Join(err, request.Body.Close())
		}
		return nil, err
	}
	return d.next.RoundTrip(request)
}

func (d *downloadTransport) CloseIdleConnections() {
	if closer, ok := d.next.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}
