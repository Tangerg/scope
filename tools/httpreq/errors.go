package httpreq

import "errors"

var (
	ErrNilClient             = errors.New("httpreq: client must not be nil")
	ErrNilRequest            = errors.New("httpreq: request must not be nil")
	ErrMissingAllowedHosts   = errors.New("httpreq: allowed hosts must not be empty; configure an explicit network allowlist")
	ErrInvalidClientConfig   = errors.New("httpreq: client configuration is invalid")
	ErrInvalidHostPattern    = errors.New("httpreq: host pattern is invalid")
	ErrEmptyURL              = errors.New("httpreq: url must not be empty")
	ErrInvalidURL            = errors.New("httpreq: url must be an absolute http(s) URL")
	ErrInvalidMethod         = errors.New("httpreq: method must be GET, HEAD, POST, PUT, PATCH, or DELETE")
	ErrInvalidRequestTimeout = errors.New("httpreq: timeout_ms must be between 1 and 120000 when set")
	ErrHostNotAllowed        = errors.New("httpreq: host is not allowed by client policy")
	ErrHostHeaderOverride    = errors.New("httpreq: model-supplied Host header overrides are prohibited")
	ErrMethodNotAllowed      = errors.New("httpreq: method is not allowed by client policy")
	ErrRedirectLimitReached  = errors.New("httpreq: redirect limit reached")
)
