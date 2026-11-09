package httpreq

import (
	"cmp"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/net/idna"
)

const defaultRedirectLimit = 10

const (
	httpScheme       = "http"
	httpsScheme      = "https"
	httpDefaultPort  = 80
	httpsDefaultPort = 443
)

// Allowlist is a compiled authority policy. A pattern without a port admits only
// the request scheme's default port, so allowing a host never opens every
// service it runs. Exact and leading-wildcard patterns share the same case,
// trailing-dot, IP, and IDNA normalization as request hosts. The zero value
// allows nothing.
type Allowlist struct {
	patterns []authorityPattern
}

func NewAllowlist(patterns []string) (Allowlist, error) {
	compiled := make([]authorityPattern, 0, len(patterns))
	for index, raw := range patterns {
		pattern, err := parseAuthorityPattern(raw)
		if err != nil {
			return Allowlist{}, fmt.Errorf("host pattern %d: %w", index, err)
		}
		compiled = append(compiled, pattern)
	}
	return Allowlist{patterns: compiled}, nil
}

// Allows reports whether target's host and effective port match a pattern.
func (a Allowlist) Allows(target *url.URL) bool {
	destination, ok := newDestination(target)
	if !ok {
		return false
	}
	return slices.ContainsFunc(a.patterns, func(pattern authorityPattern) bool {
		return pattern.admits(destination)
	})
}

// destination is a request target in the form patterns compare.
type destination struct {
	host        string
	address     bool
	port        uint16
	defaultPort uint16
}

func newDestination(target *url.URL) (destination, bool) {
	if target == nil {
		return destination{}, false
	}
	defaultPort, ok := schemeDefaultPort(target.Scheme)
	if !ok {
		return destination{}, false
	}
	host, err := normalizeHost(target.Hostname())
	if err != nil {
		return destination{}, false
	}
	port := defaultPort
	if raw := target.Port(); raw != "" {
		if port, err = parsePort(raw); err != nil {
			return destination{}, false
		}
	}
	_, parseErr := netip.ParseAddr(host)
	return destination{host: host, address: parseErr == nil, port: port, defaultPort: defaultPort}, true
}

func schemeDefaultPort(scheme string) (uint16, bool) {
	switch scheme {
	case httpScheme:
		return httpDefaultPort, true
	case httpsScheme:
		return httpsDefaultPort, true
	default:
		return 0, false
	}
}

type authorityPattern struct {
	exact  string
	suffix string
	// port zero admits only the request scheme's default port.
	port uint16
}

// admits keeps IP addresses exact-only: a dotted IPv4 or IPv4-mapped address
// would otherwise satisfy a numeric wildcard suffix such as "*.0.1".
func (a authorityPattern) admits(target destination) bool {
	if target.port != cmp.Or(a.port, target.defaultPort) {
		return false
	}
	if a.exact != "" {
		return a.exact == target.host
	}
	return !target.address && strings.HasSuffix(target.host, a.suffix)
}

func parseAuthorityPattern(raw string) (authorityPattern, error) {
	candidate := strings.TrimSpace(raw)
	if candidate == "" {
		return authorityPattern{}, fmt.Errorf("%w: host is empty", ErrInvalidHostPattern)
	}
	host, port, err := splitPatternPort(candidate)
	if err != nil {
		return authorityPattern{}, fmt.Errorf("%w %q: %w", ErrInvalidHostPattern, raw, err)
	}
	if suffix, wildcard := strings.CutPrefix(host, "*."); wildcard {
		normalized, normalizeErr := normalizeHost(suffix)
		if normalizeErr != nil {
			return authorityPattern{}, fmt.Errorf("%w %q: %w", ErrInvalidHostPattern, raw, normalizeErr)
		}
		if _, addressErr := netip.ParseAddr(normalized); addressErr == nil {
			return authorityPattern{}, fmt.Errorf("%w %q: wildcard IP addresses are not supported", ErrInvalidHostPattern, raw)
		}
		return authorityPattern{suffix: "." + normalized, port: port}, nil
	}
	if strings.Contains(host, "*") {
		return authorityPattern{}, fmt.Errorf("%w %q: only a leading '*.' wildcard is supported", ErrInvalidHostPattern, raw)
	}
	normalized, err := normalizeHost(host)
	if err != nil {
		return authorityPattern{}, fmt.Errorf("%w %q: %w", ErrInvalidHostPattern, raw, err)
	}
	return authorityPattern{exact: normalized, port: port}, nil
}

// splitPatternPort leaves a bare IPv6 literal whole: its colons are not a port
// separator, which is why a port after an IPv6 address requires brackets.
func splitPatternPort(pattern string) (string, uint16, error) {
	if _, err := netip.ParseAddr(pattern); err == nil {
		return pattern, 0, nil
	}
	host, rawPort, err := net.SplitHostPort(pattern)
	if err != nil {
		return pattern, 0, nil
	}
	port, err := parsePort(rawPort)
	if err != nil {
		return "", 0, err
	}
	return host, port, nil
}

func parsePort(raw string) (uint16, error) {
	port, err := strconv.ParseUint(raw, 10, 16)
	if err != nil || port == 0 {
		return 0, fmt.Errorf("port %q must be between 1 and 65535", raw)
	}
	return uint16(port), nil
}

func normalizeHost(raw string) (string, error) {
	host := strings.TrimSuffix(strings.TrimSpace(raw), ".")
	if host == "" {
		return "", fmt.Errorf("%w: host is empty", ErrInvalidHostPattern)
	}
	if strings.HasPrefix(host, "[") || strings.HasSuffix(host, "]") {
		if !strings.HasPrefix(host, "[") || !strings.HasSuffix(host, "]") {
			return "", fmt.Errorf("%w: malformed IP literal", ErrInvalidHostPattern)
		}
		host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	}
	if address, err := netip.ParseAddr(host); err == nil {
		return address.String(), nil
	}
	if strings.ContainsAny(host, ":/?#@") {
		return "", fmt.Errorf("%w: expected a hostname with an optional port, not a URL", ErrInvalidHostPattern)
	}
	ascii, err := idna.Lookup.ToASCII(host)
	if err != nil {
		return "", fmt.Errorf("%w: normalize IDNA hostname: %w", ErrInvalidHostPattern, err)
	}
	return strings.ToLower(strings.TrimSuffix(ascii, ".")), nil
}
