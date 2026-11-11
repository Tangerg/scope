package httpclient

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
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

// ResponseBudget is one aggregate byte grant shared by every response of an
// operation, such as the pages of a listing, so a scan that follows a server's
// pagination cannot read more in total than its caller granted. Each body is
// read in full against what remains; a successful body must be one JSON value.
// It is not safe for concurrent use.
type ResponseBudget struct {
	remaining int64
}

func NewResponseBudget(maxBytes int64) (*ResponseBudget, error) {
	if maxBytes <= 0 {
		return nil, errors.New("httpclient: response budget must be positive")
	}
	return &ResponseBudget{remaining: maxBytes}, nil
}

func (r *ResponseBudget) Apply(request *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {
	if next == nil {
		return nil, errors.New("httpclient: next request handler must not be nil")
	}
	response, err := next(request)
	if err != nil || response == nil || response.Body == nil {
		return response, err
	}
	body := http.MaxBytesReader(nil, response.Body, r.remaining)
	content, readErr := io.ReadAll(body)
	if err := errors.Join(readErr, body.Close()); err != nil {
		return nil, fmt.Errorf("httpclient: read budgeted response: %w", err)
	}
	r.remaining -= int64(len(content))
	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices && !jsontext.Value(content).IsValid() {
		return nil, errors.New("httpclient: budgeted response is not valid JSON")
	}
	response.Body = io.NopCloser(bytes.NewReader(content))
	return response, nil
}
