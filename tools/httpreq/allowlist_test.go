package httpreq

import "testing"

func TestAllowlistWildcardDoesNotMatchIPAddresses(t *testing.T) {
	allowlist, err := NewAllowlist([]string{"*.0.1", "*.1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"10.0.0.1", "127.0.0.1", "::ffff:10.0.0.1"} {
		if allowlist.Allows(host) {
			t.Errorf("wildcard pattern allowed IP address %q", host)
		}
	}
	if !allowlist.Allows("service.0.1") {
		t.Error("wildcard pattern rejected matching hostname")
	}
}
