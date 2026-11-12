package httpclient_test

import (
	"net/url"
	"testing"

	"github.com/Tangerg/scope/core/httpclient"
)

func TestPathSegmentStaysOneSegmentUnderResolution(t *testing.T) {
	base, err := url.Parse("https://api.example.test/v1/items/")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ value, segment string }{
		{"plain-id_1.0", "plain-id_1.0"},
		{".", "%2E"},
		{"..", "%2E%2E"},
		{"...", "..."},
		{"a/b", "a%2Fb"},
		{`a\b`, "a%5Cb"},
		{"a?b#c", "a%3Fb%23c"},
		{"%2e%2e", "%252e%252e"},
		{"文档", "%E6%96%87%E6%A1%A3"},
		{"", ""},
	} {
		segment := httpclient.PathSegment(test.value)
		if segment != test.segment {
			t.Errorf("PathSegment(%q) = %q, want %q", test.value, segment, test.segment)
		}
		if test.value == "" {
			continue
		}
		reference, err := url.Parse(segment + "/tail")
		if err != nil {
			t.Fatal(err)
		}
		resolved := base.ResolveReference(reference)
		if want := "/v1/items/" + test.segment + "/tail"; resolved.EscapedPath() != want {
			t.Errorf("resolved %q = %q, want %q", test.value, resolved.EscapedPath(), want)
		}
		decoded, err := url.PathUnescape(segment)
		if err != nil || decoded != test.value {
			t.Errorf("PathUnescape(%q) = %q, %v", segment, decoded, err)
		}
	}
}
