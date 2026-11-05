package httpreq

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
)

func TestAddressPolicyAdmitsOnlyPublicAndAllowedNetworks(t *testing.T) {
	policy, err := newAddressPolicy([]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		address string
		want    bool
	}{
		{"8.8.8.8", true},
		{"2606:4700:4700::1111", true},
		{"::ffff:8.8.8.8", true},
		{"10.1.2.3", true},
		{"::ffff:10.1.2.3", true},
		{"127.0.0.1", false},
		{"::1", false},
		{"::ffff:127.0.0.1", false},
		{"172.16.0.1", false},
		{"192.168.1.1", false},
		{"169.254.169.254", false},
		{"100.64.0.1", false},
		{"0.0.0.0", false},
		{"0.1.2.3", false},
		{"192.0.0.170", false},
		{"198.18.0.1", false},
		{"240.0.0.1", false},
		{"255.255.255.255", false},
		{"224.0.0.1", false},
		{"::", false},
		{"::127.0.0.1", false},
		{"fe80::1%en0", false},
		{"fc00::1", false},
		{"fd00::1", false},
		{"fec0::1", false},
		{"64:ff9b::7f00:1", false},
		{"64:ff9b:1::a00:1", false},
		{"2001::1", false},
		{"2002:7f00:1::1", false},
	} {
		if got := policy.admits(netip.MustParseAddr(test.address)); got != test.want {
			t.Errorf("admits(%s) = %v, want %v", test.address, got, test.want)
		}
	}
	if err := policy.control("tcp", "not-an-address", nil); !errors.Is(err, ErrAddressNotAllowed) {
		t.Errorf("control admitted an unparsable address: %v", err)
	}
}

func TestOwnTransportRefusesAddressesOutsidePolicy(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, "internal")
	}))
	t.Cleanup(server.Close)
	authority := server.Listener.Addr().String()
	byName := testURLWithHostname(t, server.URL, "localhost")

	refusing, err := NewClient(ClientConfig{AllowedHosts: []string{authority, testURLHost(t, byName)}})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{server.URL, byName} {
		if _, refusal := refusing.Do(t.Context(), &Request{URL: target}); !errors.Is(refusal, ErrAddressNotAllowed) {
			t.Fatalf("request to %s = %v, want ErrAddressNotAllowed", target, refusal)
		}
	}
	if hits.Load() != 0 {
		t.Fatal("a refused address received a request")
	}

	admitting, err := NewClient(ClientConfig{AllowedHosts: []string{authority}, AllowedNetworks: loopback})
	if err != nil {
		t.Fatal(err)
	}
	response, err := admitting.Do(t.Context(), &Request{URL: server.URL})
	if err != nil || string(response.Body.Bytes()) != "internal" || hits.Load() != 1 {
		t.Fatalf("allowed network: response=%+v err=%v hits=%d", response, err, hits.Load())
	}
}

func TestRedirectCannotReachAnAddressOutsidePolicy(t *testing.T) {
	listener, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}
	var hits atomic.Int32
	target := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	target.Listener = listener
	target.Start()
	t.Cleanup(target.Close)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	t.Cleanup(source.Close)

	client, err := NewClient(ClientConfig{
		AllowedHosts:    []string{testURLHost(t, source.URL), testURLHost(t, target.URL)},
		AllowedNetworks: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Do(t.Context(), &Request{URL: source.URL}); !errors.Is(err, ErrAddressNotAllowed) {
		t.Fatalf("redirect to IPv6 loopback = %v, want ErrAddressNotAllowed", err)
	}
	if hits.Load() != 0 {
		t.Fatal("the redirect reached an address outside policy")
	}
}
