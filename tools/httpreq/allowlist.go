package httpreq

import (
	"fmt"
	"net/netip"
	"strings"

	"golang.org/x/net/idna"
)

const defaultRedirectLimit = 10

// Allowlist is a compiled host policy. Exact and leading-wildcard patterns
// share the same case, trailing-dot, IP, and IDNA normalization as request
// hosts. The zero value allows nothing.
type Allowlist struct {
	patterns []hostPattern
}

// NewAllowlist compiles host patterns once so every request is matched against
// normalized forms rather than raw strings. Comparing hosts textually is what
// lets case, a trailing dot, or an IDNA variant slip past a filter that looks
// correct.
func NewAllowlist(hosts []string) (Allowlist, error) {
	patterns := make([]hostPattern, 0, len(hosts))
	for index, host := range hosts {
		pattern, err := parseHostPattern(host)
		if err != nil {
			return Allowlist{}, fmt.Errorf("host pattern %d: %w", index, err)
		}
		patterns = append(patterns, pattern)
	}
	return Allowlist{patterns: patterns}, nil
}

func (a Allowlist) Allows(host string) bool {
	normalized, err := normalizeHost(host)
	if err != nil {
		return false
	}
	for _, pattern := range a.patterns {
		if pattern.exact == normalized ||
			(pattern.suffix != "" && strings.HasSuffix(normalized, pattern.suffix)) {
			return true
		}
	}
	return false
}

type hostPattern struct {
	exact  string
	suffix string
}

func parseHostPattern(raw string) (hostPattern, error) {
	candidate := strings.TrimSpace(raw)
	if candidate == "" {
		return hostPattern{}, fmt.Errorf("%w: host is empty", ErrInvalidHostPattern)
	}
	if strings.HasPrefix(candidate, "*.") {
		host, err := normalizeHost(candidate[2:])
		if err != nil {
			return hostPattern{}, fmt.Errorf("%w %q: %w", ErrInvalidHostPattern, raw, err)
		}
		if _, err := netip.ParseAddr(host); err == nil {
			return hostPattern{}, fmt.Errorf("%w %q: wildcard IP addresses are not supported", ErrInvalidHostPattern, raw)
		}
		return hostPattern{suffix: "." + host}, nil
	}
	if strings.Contains(candidate, "*") {
		return hostPattern{}, fmt.Errorf("%w %q: only a leading '*.' wildcard is supported", ErrInvalidHostPattern, raw)
	}
	host, err := normalizeHost(candidate)
	if err != nil {
		return hostPattern{}, fmt.Errorf("%w %q: %w", ErrInvalidHostPattern, raw, err)
	}
	return hostPattern{exact: host}, nil
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
	if strings.Contains(host, ":") {
		return "", fmt.Errorf("%w: ports are not allowed", ErrInvalidHostPattern)
	}
	if strings.ContainsAny(host, "/?#@") {
		return "", fmt.Errorf("%w: expected a hostname, not a URL", ErrInvalidHostPattern)
	}
	ascii, err := idna.Lookup.ToASCII(host)
	if err != nil {
		return "", fmt.Errorf("%w: normalize IDNA hostname: %w", ErrInvalidHostPattern, err)
	}
	return strings.ToLower(strings.TrimSuffix(ascii, ".")), nil
}
