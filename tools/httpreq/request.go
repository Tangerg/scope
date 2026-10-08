package httpreq

import (
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Request struct {
	URL       string            `json:"url" jsonschema:"minLength=1" jsonschema_description:"Absolute http(s) URL. Host must match the configured allowlist."`
	Method    Method            `json:"method,omitempty" jsonschema:"enum=GET,enum=HEAD,enum=POST,enum=PUT,enum=PATCH,enum=DELETE" jsonschema_description:"HTTP method: GET (default), HEAD, POST, PUT, PATCH, or DELETE. Must be in the configured method allowlist."`
	Headers   map[string]string `json:"headers,omitempty" jsonschema_description:"Optional request headers. Values here override this tool's configured default headers. Host overrides are prohibited; authority comes from the URL."`
	Query     map[string]string `json:"query,omitempty" jsonschema_description:"Optional query parameters appended to the URL."`
	Body      string            `json:"body,omitempty" jsonschema_description:"Optional request body for POST, PUT, PATCH, or DELETE; GET and HEAD carry none. For JSON, pass a JSON-encoded string and set Content-Type via Headers."`
	TimeoutMS int               `json:"timeout_ms,omitzero" jsonschema:"minimum=1,maximum=120000" jsonschema_description:"Per-call timeout in milliseconds, from 1 to 120000. Omit to use the configured default."`
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

func (r *Request) timeout(fallback time.Duration) time.Duration {
	if r.TimeoutMS > 0 {
		return time.Duration(r.TimeoutMS) * time.Millisecond
	}
	return fallback
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
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return ErrInvalidURL
	}
	method, err := r.Method.Normalize()
	if err != nil {
		return err
	}
	if r.Body != "" && !method.carriesBody() {
		return fmt.Errorf("%w: %s", ErrBodyNotAllowed, method)
	}
	for name := range r.Headers {
		if http.CanonicalHeaderKey(name) == "Host" {
			return ErrHostHeaderOverride
		}
	}
	if err := validateHeaderFields(r.Headers); err != nil {
		return err
	}
	if r.TimeoutMS < 0 || r.TimeoutMS > int(MaxRequestTimeout/time.Millisecond) {
		return ErrInvalidRequestTimeout
	}
	return nil
}

// validateHeaderFields admits one entry per HTTP field. A field name is
// case-insensitive, so two keys naming one field would leave map iteration
// order to choose the value sent.
func validateHeaderFields(headers map[string]string) error {
	names := make(map[string]string, len(headers))
	for name := range headers {
		field := http.CanonicalHeaderKey(name)
		if previous, duplicate := names[field]; duplicate {
			return fmt.Errorf("%w: %q and %q", ErrDuplicateHeader, previous, name)
		}
		names[field] = name
	}
	return nil
}
