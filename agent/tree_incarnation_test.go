package agent

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"
)

func TestTreeIncarnationIDStrictRoundTrip(t *testing.T) {
	id := newTreeIncarnationID()
	if !id.Valid() {
		t.Fatal("new TreeIncarnationID is invalid")
	}
	data, err := jsonv2.Marshal(id)
	if err != nil {
		t.Fatal(err)
	}
	var decoded TreeIncarnationID
	if err := jsonv2.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != id {
		t.Fatalf("decoded identity = %s, want %s", decoded, id)
	}
}

func TestTreeIncarnationIDRejectsNoncanonicalValues(t *testing.T) {
	for _, value := range []string{
		"",
		"incarnation:short",
		"incarnation:0123456789ABCDEF0123456789ABCDEF",
		"generation:0123456789abcdef0123456789abcdef",
	} {
		if _, err := parseTreeIncarnationID(value); !errors.Is(err, ErrInvalidTreeIncarnationID) {
			t.Fatalf("parseTreeIncarnationID(%q) error = %v", value, err)
		}
	}
}
