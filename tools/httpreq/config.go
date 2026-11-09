package httpreq

import (
	"cmp"
	"fmt"
	"math"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/samber/lo"
)

const (
	DefaultTimeout          = 30 * time.Second
	DefaultMaxResponseBytes = int64(256 * 1024)
	MaxRequestTimeout       = 2 * time.Minute
)

const maxSupportedResponseBytes = int64(math.MaxInt64 - 1)

// ClientConfig requires AllowedHosts; there is no default network access.
type ClientConfig struct {
	// AllowedHosts accepts exact hosts and one leading wildcard, each with an
	// optional port, such as "api.example.com", "*.example.com" or
	// "localhost:8080". A wildcard does not match its root. A pattern without a
	// port admits only the request scheme's default port.
	AllowedHosts []string

	// AllowedNetworks admits connections to addresses outside public unicast
	// space, such as 127.0.0.0/8 for a loopback service. Without a matching
	// network the client refuses private, loopback, link-local and other
	// special-purpose addresses, including those a permitted host resolves to.
	// It governs only the client's own transport.
	AllowedNetworks []netip.Prefix

	// AllowedMethods defaults to GET and HEAD. Comparison is case-insensitive.
	AllowedMethods []Method

	// DefaultHeaders are added unless [Request.Headers] overrides them.
	DefaultHeaders map[string]string

	// MaxResponseBytes selects [DefaultMaxResponseBytes] at zero.
	MaxResponseBytes int64

	// DefaultTimeout selects [DefaultTimeout] at zero.
	DefaultTimeout time.Duration

	// HTTPClient supplies caller-owned redirect policy, cookie jar, and
	// timeout. NewClient clones the value before installing its own redirect
	// policy. A nil Transport selects the client's own, which connects directly
	// through the address policy. A supplied Transport decides where its
	// connections go, so it cannot be combined with AllowedNetworks.
	HTTPClient *http.Client
}

type clientPolicy struct {
	allowedHosts     Allowlist
	addresses        addressPolicy
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
	if request == nil || request.URL == nil || request.URL.User != nil {
		return fmt.Errorf("httpreq: validate redirect target: %w", ErrInvalidURL)
	}
	if _, supported := schemeDefaultPort(request.URL.Scheme); !supported {
		return fmt.Errorf("httpreq: validate redirect target: %w", ErrInvalidURL)
	}
	if !c.allowedHosts.Allows(request.URL) {
		return fmt.Errorf("%w: redirect target %q", ErrHostNotAllowed, request.URL.Host)
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
	if err := validateHeaderFields(c.DefaultHeaders); err != nil {
		return clientPolicy{}, fmt.Errorf("%w: default headers: %w", ErrInvalidClientConfig, err)
	}
	allowedHosts, err := NewAllowlist(c.AllowedHosts)
	if err != nil {
		return clientPolicy{}, fmt.Errorf("%w: allowed hosts: %w", ErrInvalidClientConfig, err)
	}
	if len(c.AllowedNetworks) != 0 && c.HTTPClient != nil && !lo.IsNil(c.HTTPClient.Transport) {
		return clientPolicy{}, fmt.Errorf("%w: allowed networks govern only the client's own transport", ErrInvalidClientConfig)
	}
	addresses, err := newAddressPolicy(c.AllowedNetworks)
	if err != nil {
		return clientPolicy{}, err
	}
	allowedMethods, err := c.compileAllowedMethods()
	if err != nil {
		return clientPolicy{}, err
	}
	return clientPolicy{
		allowedHosts:     allowedHosts,
		addresses:        addresses,
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
