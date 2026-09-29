package httpreq

import (
	"cmp"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"
)

const (
	DefaultTimeout          = 30 * time.Second
	DefaultMaxResponseBytes = int64(256 * 1024)
	MaxRequestTimeout       = 2 * time.Minute
)

const maxSupportedResponseBytes = int64(math.MaxInt64 - 1)

// ClientConfig requires AllowedHosts; there is no default network access.
type ClientConfig struct {
	// AllowedHosts accepts exact hosts and one leading wildcard, such as
	// "api.example.com" or "*.example.com". A wildcard does not match its root.
	AllowedHosts []string

	// AllowedMethods defaults to GET and HEAD. Comparison is case-insensitive.
	AllowedMethods []Method

	// DefaultHeaders are added unless [Request.Headers] overrides them.
	DefaultHeaders map[string]string

	// MaxResponseBytes selects [DefaultMaxResponseBytes] at zero.
	MaxResponseBytes int64

	// DefaultTimeout selects [DefaultTimeout] at zero.
	DefaultTimeout time.Duration

	// HTTPClient supplies caller-owned transport, cookie jar, proxy, and TLS
	// settings. NewClient clones the value before installing redirect policy.
	HTTPClient *http.Client
}

type clientPolicy struct {
	allowedHosts     Allowlist
	allowedMethods   map[Method]struct{}
	maxResponseBytes int64
	defaultTimeout   time.Duration
}

func (c clientPolicy) allowsMethod(method Method) bool {
	_, allowed := c.allowedMethods[method]
	return allowed
}

func (c clientPolicy) checkRedirect(request *http.Request, via []*http.Request) error {
	if len(via) >= defaultRedirectLimit {
		return fmt.Errorf("%w: limit %d", ErrRedirectLimitReached, defaultRedirectLimit)
	}
	if request == nil || request.URL == nil {
		return fmt.Errorf("httpreq: validate redirect target: %w", ErrInvalidURL)
	}
	if request.URL.Scheme != "http" && request.URL.Scheme != "https" {
		return fmt.Errorf("httpreq: validate redirect target: %w", ErrInvalidURL)
	}
	host := request.URL.Hostname()
	if !c.allowedHosts.Allows(host) {
		return fmt.Errorf("%w: redirect target %q", ErrHostNotAllowed, host)
	}
	method, err := Method(request.Method).Normalize()
	if err != nil {
		return fmt.Errorf("%w: redirect method %q: %w", ErrMethodNotAllowed, request.Method, err)
	}
	if !c.allowsMethod(method) {
		return fmt.Errorf("%w: redirect method %s", ErrMethodNotAllowed, method)
	}
	return nil
}

func (c ClientConfig) Validate() error {
	_, err := c.compilePolicy()
	return err
}

func (c ClientConfig) compilePolicy() (clientPolicy, error) {
	if len(c.AllowedHosts) == 0 {
		return clientPolicy{}, fmt.Errorf("%w: %w", ErrInvalidClientConfig, ErrMissingAllowedHosts)
	}
	if c.DefaultTimeout < 0 || c.DefaultTimeout > MaxRequestTimeout {
		return clientPolicy{}, fmt.Errorf("%w: default timeout must be between 0 and %s", ErrInvalidClientConfig, MaxRequestTimeout)
	}
	if c.MaxResponseBytes < 0 || c.MaxResponseBytes > maxSupportedResponseBytes {
		return clientPolicy{}, fmt.Errorf("%w: maximum response bytes must be between 0 and %d", ErrInvalidClientConfig, maxSupportedResponseBytes)
	}
	allowedHosts, err := NewAllowlist(c.AllowedHosts)
	if err != nil {
		return clientPolicy{}, fmt.Errorf("%w: allowed hosts: %w", ErrInvalidClientConfig, err)
	}
	allowedMethods, err := c.compileAllowedMethods()
	if err != nil {
		return clientPolicy{}, err
	}
	return clientPolicy{
		allowedHosts:     allowedHosts,
		allowedMethods:   allowedMethods,
		maxResponseBytes: cmp.Or(c.MaxResponseBytes, DefaultMaxResponseBytes),
		defaultTimeout:   cmp.Or(c.DefaultTimeout, DefaultTimeout),
	}, nil
}

func (c ClientConfig) compileAllowedMethods() (map[Method]struct{}, error) {
	methods := c.AllowedMethods
	if len(methods) == 0 {
		methods = []Method{MethodGET, MethodHEAD}
	}
	allowedMethods := make(map[Method]struct{}, len(methods))
	for index, method := range methods {
		// A blank method would otherwise normalize to the GET wire default.
		if strings.TrimSpace(string(method)) == "" {
			return nil, fmt.Errorf("%w: allowed method %d is blank", ErrInvalidClientConfig, index)
		}
		normalized, err := method.Normalize()
		if err != nil {
			return nil, fmt.Errorf("%w: allowed method %d %q: %w", ErrInvalidClientConfig, index, method, err)
		}
		allowedMethods[normalized] = struct{}{}
	}
	return allowedMethods, nil
}
