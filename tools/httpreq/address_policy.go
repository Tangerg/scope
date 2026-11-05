package httpreq

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"syscall"
	"time"
)

// Requests are bounded by their own deadlines; this only releases connections
// that no later request reuses.
const idleConnTimeout = 90 * time.Second

// nonPublicNetworks are IANA special-purpose ranges that the netip classifiers
// do not already exclude and through which a connection can reach this host or
// a private network, including the IPv6 forms that embed an arbitrary IPv4
// address.
var nonPublicNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("fec0::/10"),
}

// addressPolicy decides which addresses the client's own transport connects to.
// The allowlist judges names before a request is sent, but only the dialer sees
// the address DNS answered with, so this is the one place where a permitted name
// that resolves inward, by misconfiguration or rebinding, can be refused.
type addressPolicy struct {
	allowed []netip.Prefix
}

func newAddressPolicy(allowed []netip.Prefix) (addressPolicy, error) {
	for index, network := range allowed {
		if !network.IsValid() {
			return addressPolicy{}, fmt.Errorf("%w: allowed network %d is invalid", ErrInvalidClientConfig, index)
		}
	}
	return addressPolicy{allowed: slices.Clone(allowed)}, nil
}

func (a addressPolicy) admits(address netip.Addr) bool {
	// A zoned address never matches a prefix, and a mapped one must be judged by
	// the IPv4 address it carries.
	address = address.WithZone("").Unmap()
	if publicAddress(address) {
		return true
	}
	return slices.ContainsFunc(a.allowed, func(network netip.Prefix) bool { return network.Contains(address) })
}

func publicAddress(address netip.Addr) bool {
	if !address.IsGlobalUnicast() || address.IsPrivate() {
		return false
	}
	return !slices.ContainsFunc(nonPublicNetworks, func(network netip.Prefix) bool { return network.Contains(address) })
}

// control runs after resolution and before connecting, so a refused address
// receives no packet and cannot serve as a port-scanning oracle.
func (a addressPolicy) control(_, address string, _ syscall.RawConn) error {
	endpoint, err := netip.ParseAddrPort(address)
	if err != nil || !a.admits(endpoint.Addr()) {
		return fmt.Errorf("%w: %s", ErrAddressNotAllowed, address)
	}
	return nil
}

// transport connects directly: a proxy would choose the destination address
// beyond the reach of this policy.
func (a addressPolicy) transport() *http.Transport {
	dialer := &net.Dialer{Control: a.control}
	return &http.Transport{DialContext: dialer.DialContext, ForceAttemptHTTP2: true, IdleConnTimeout: idleConnTimeout}
}
