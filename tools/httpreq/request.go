package httpreq

import (
	"maps"
	"net/url"
	"strings"
	"time"
)

// Request is the model-facing argument shape, and its struct tags are the
// contract the model actually sees. The schema is derived from this type so
// the description a model reads and the fields the tool decodes cannot drift
// apart.
type Request struct {
	URL       string            `json:"url" jsonschema:"minLength=1" jsonschema_description:"Absolute http(s) URL. Host must match the configured allowlist."`
	Method    Method            `json:"method,omitempty" jsonschema:"enum=GET,enum=HEAD,enum=POST,enum=PUT,enum=PATCH,enum=DELETE" jsonschema_description:"HTTP method: GET (default), HEAD, POST, PUT, PATCH, or DELETE. Must be in the configured method allowlist."`
	Headers   map[string]string `json:"headers,omitempty" jsonschema_description:"Optional request headers. Values here override this tool's configured default headers."`
	Query     map[string]string `json:"query,omitempty" jsonschema_description:"Optional query parameters appended to the URL."`
	Body      string            `json:"body,omitempty" jsonschema_description:"Optional request body — for JSON, pass a JSON-encoded string and set Content-Type via Headers."`
	TimeoutMS int               `json:"timeout_ms,omitempty" jsonschema:"minimum=1,maximum=120000" jsonschema_description:"Per-call timeout in milliseconds, from 1 to 120000. Omit to use the configured default."`
}

func (r *Request) prepare() (*Request, error) {
	if r == nil {
		return nil, ErrNilRequest
	}
	prepared := *r
	prepared.URL = strings.TrimSpace(r.URL)
	method, err := r.Method.Normalize()
	if err != nil {
		return nil, err
	}
	prepared.Method = method
	prepared.Headers = maps.Clone(r.Headers)
	prepared.Query = maps.Clone(r.Query)
	if err := prepared.Validate(); err != nil {
		return nil, err
	}
	return &prepared, nil
}

func (r *Request) Validate() error {
	if r == nil {
		return ErrNilRequest
	}
	trimmedURL := strings.TrimSpace(r.URL)
	if trimmedURL == "" {
		return ErrEmptyURL
	}
	parsed, err := url.Parse(trimmedURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return ErrInvalidURL
	}
	if err := r.Method.Validate(); err != nil {
		return err
	}
	if r.TimeoutMS < 0 || r.TimeoutMS > int(MaxRequestTimeout/time.Millisecond) {
		return ErrInvalidRequestTimeout
	}
	return nil
}
