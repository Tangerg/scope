package httpclient

import (
	"errors"
	"net/http"
)

// ResponseBodyLimit admits bytes as they are read, before a decoder can allocate
// for an untrusted response. The HTTP client still owns retries and body closure.
type ResponseBodyLimit int64

func (r ResponseBodyLimit) Apply(request *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {
	return r.apply(request, next, false)
}

// Successful streaming bodies have a per-event contract, so their error
// envelopes must be bounded without imposing a limit on the entire stream.
func (r ResponseBodyLimit) ApplyErrors(request *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {
	return r.apply(request, next, true)
}

// For SDKs with LF/CRLF SSE framing, each wire event has its own budget,
// including comments, BOM, and delimiters. Empty lines reset admission, so
// long-lived streams retain backpressure without a total byte ceiling. This
// wrapper preserves bytes; the SDK remains responsible for interpreting them.
func (r ResponseBodyLimit) ApplyEventStream(request *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {
	response, err := r.ApplyErrors(request, next)
	if err != nil || response == nil || response.Body == nil || response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return response, err
	}
	response.Body = &eventStreamBody{source: response.Body, limit: int64(r)}
	return response, nil
}

func (r ResponseBodyLimit) apply(request *http.Request, next func(*http.Request) (*http.Response, error), errorsOnly bool) (*http.Response, error) {
	if r <= 0 {
		return nil, errors.New("httpclient: response body limit must be positive")
	}
	if next == nil {
		return nil, errors.New("httpclient: next request handler must not be nil")
	}
	response, err := next(request)
	if err != nil || response == nil || response.Body == nil {
		return response, err
	}
	if errorsOnly && response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		return response, nil
	}
	response.Body = http.MaxBytesReader(nil, response.Body, int64(r))
	return response, nil
}
