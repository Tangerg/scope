package httpreq

import (
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"
)

// Exported defaults keep constructor behavior visible and overridable.
const (
	DefaultTimeout          = 30 * time.Second
	DefaultMaxResponseBytes = int64(256 * 1024)
	MaxRequestTimeout       = 2 * time.Minute
)

const maxSupportedResponseBytes = int64(math.MaxInt64 - 1)

// ClientConfig defines the network authority and resource bounds frozen into
// a Client. AllowedHosts is mandatory because the zero policy denies network
// access rather than silently opening it.
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
	method := Method(request.Method).Normalize()
	if _, allowed := c.allowedMethods[method]; !allowed {
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
	allowedHosts, err := NewAllowlist(c.AllowedHosts)
	if err != nil {
		return clientPolicy{}, fmt.Errorf("%w: allowed hosts: %w", ErrInvalidClientConfig, err)
	}

	methods := c.AllowedMethods
	if len(methods) == 0 {
		methods = []Method{MethodGET, MethodHEAD}
	}
	allowedMethods := make(map[Method]struct{}, len(methods))
	for index, method := range methods {
		if strings.TrimSpace(string(method)) == "" {
			return clientPolicy{}, fmt.Errorf("%w: allowed method %d is blank", ErrInvalidClientConfig, index)
		}
		if err := method.Validate(); err != nil {
			return clientPolicy{}, fmt.Errorf(
				"%w: allowed method %d %q: %w",
				ErrInvalidClientConfig,
				index,
				method,
				err,
			)
		}
		allowedMethods[method.Normalize()] = struct{}{}
	}
	if c.DefaultTimeout < 0 || c.DefaultTimeout > MaxRequestTimeout {
		return clientPolicy{}, fmt.Errorf("%w: default timeout must be between 0 and %s", ErrInvalidClientConfig, MaxRequestTimeout)
	}
	if c.MaxResponseBytes < 0 || c.MaxResponseBytes > maxSupportedResponseBytes {
		return clientPolicy{}, fmt.Errorf("%w: maximum response bytes must be between 0 and %d", ErrInvalidClientConfig, maxSupportedResponseBytes)
	}

	maxResponseBytes := c.MaxResponseBytes
	if maxResponseBytes == 0 {
		maxResponseBytes = DefaultMaxResponseBytes
	}
	defaultTimeout := c.DefaultTimeout
	if defaultTimeout == 0 {
		defaultTimeout = DefaultTimeout
	}
	return clientPolicy{
		allowedHosts:     allowedHosts,
		allowedMethods:   allowedMethods,
		maxResponseBytes: maxResponseBytes,
		defaultTimeout:   defaultTimeout,
	}, nil
}
