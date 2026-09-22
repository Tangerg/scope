package agent

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"
)

func TestDigestStrictRoundTrip(t *testing.T) {
	want := ComputeDigest([]byte("deployment content"))
	parsed, err := ParseDigest(want.String())
	if err != nil {
		t.Fatal(err)
	}
	if parsed != want {
		t.Fatalf("ParseDigest() = %q, want %q", parsed, want)
	}
	data, err := jsonv2.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Digest
	if err := jsonv2.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != want {
		t.Fatalf("decoded Digest = %q, want %q", decoded, want)
	}
}

func TestDigestRejectsNoncanonicalValues(t *testing.T) {
	for _, value := range []string{"", "sha256:abc", "SHA256:abc", "sha256:FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF"} {
		if _, err := ParseDigest(value); !errors.Is(err, ErrInvalidDigest) {
			t.Fatalf("ParseDigest(%q) error = %v, want ErrInvalidDigest", value, err)
		}
	}
}
