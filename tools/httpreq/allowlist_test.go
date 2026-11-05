package httpreq

import (
	"net/url"
	"testing"
)

func TestAllowlistWildcardDoesNotMatchIPAddresses(t *testing.T) {
	allowlist, err := NewAllowlist([]string{"*.0.1", "*.1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"10.0.0.1", "127.0.0.1", "[::ffff:10.0.0.1]"} {
		if allowlist.Allows(httpsURL(host)) {
			t.Errorf("wildcard pattern allowed IP address %q", host)
		}
	}
	if !allowlist.Allows(httpsURL("service.0.1")) {
		t.Error("wildcard pattern rejected matching hostname")
	}
}

func TestAllowlistAdmitsOnlyTheNamedPort(t *testing.T) {
	allowlist, err := NewAllowlist([]string{"api.example.com", "*.example.net:8443", "localhost:8080", "[::1]:9000", "10.0.0.1:80"})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		target string
		want   bool
	}{
		{"https://api.example.com/", true},
		{"https://api.example.com:443/", true},
		{"http://api.example.com/", true},
		{"https://api.example.com:8443/", false},
		{"https://api.example.com:80/", false},
		{"http://api.example.com:443/", false},
		{"https://a.example.net:8443/", true},
		{"https://a.example.net/", false},
		{"http://localhost:8080/", true},
		{"http://localhost/", false},
		{"http://localhost:6379/", false},
		{"http://[::1]:9000/", true},
		{"http://[::1]/", false},
		{"http://10.0.0.1/", true},
		{"https://10.0.0.1/", false},
		{"ftp://api.example.com/", false},
	} {
		target, err := url.Parse(test.target)
		if err != nil {
			t.Fatal(err)
		}
		if got := allowlist.Allows(target); got != test.want {
			t.Errorf("Allows(%s) = %v, want %v", test.target, got, test.want)
		}
	}
	if allowlist.Allows(nil) {
		t.Error("Allows(nil) admitted a missing target")
	}
}
