package metadata_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/metadata"
)

func TestExtensionsOwnNamespaceAndValues(t *testing.T) {
	var extensions metadata.Extensions
	if err := extensions.Set("provider/options", map[string]any{"enabled": true}); err != nil {
		t.Fatal(err)
	}
	decoded, found, err := extensions.Decode[map[string]bool]("provider/options")
	if err != nil || !found || !decoded["enabled"] {
		t.Fatalf("Decode() = %#v, %v, %v", decoded, found, err)
	}
	if setErr := extensions.Set("invalid", true); setErr == nil {
		t.Fatal("Set accepted an unnamespaced key")
	}

	clone := extensions.Clone()
	if setErr := clone.Set("provider/options", false); setErr != nil {
		t.Fatal(setErr)
	}
	original, _, err := extensions.Decode[map[string]bool]("provider/options")
	if err != nil || !original["enabled"] {
		t.Fatal("Clone aliased the original")
	}
}

func TestExtensionsJSONRejectsInvalidKeys(t *testing.T) {
	var extensions metadata.Extensions
	if err := jsonv2.Unmarshal([]byte(`{"invalid":true}`), &extensions); err == nil {
		t.Fatal("UnmarshalJSON accepted an unnamespaced key")
	}
	if err := (*metadata.Extensions)(nil).Set("provider/value", true); !errors.Is(err, metadata.ErrNilMap) {
		t.Fatalf("nil Set error = %v", err)
	}
	if _, _, err := extensions.Decode[bool]("invalid"); err == nil {
		t.Fatal("Decode accepted an unnamespaced key")
	}
	if err := jsonv2.Unmarshal([]byte(`{"provider/value":`), &extensions); err == nil {
		t.Fatal("UnmarshalJSON accepted invalid JSON")
	}
	if err := (*metadata.Extensions)(nil).UnmarshalJSON([]byte(`{}`)); !errors.Is(err, metadata.ErrNilMap) {
		t.Fatalf("nil UnmarshalJSON error = %v", err)
	}
}

func TestExtensionsMergeEqualityAndJSON(t *testing.T) {
	var first, second metadata.Extensions
	if !first.IsZero() {
		t.Fatal("zero Extensions is not zero")
	}
	if err := first.Set("provider/first", 1); err != nil {
		t.Fatal(err)
	}
	if err := second.Set("provider/second", []string{"two"}); err != nil {
		t.Fatal(err)
	}
	if err := first.Merge(second); err != nil {
		t.Fatal(err)
	}
	if err := (*metadata.Extensions)(nil).Merge(second); !errors.Is(err, metadata.ErrNilMap) {
		t.Fatalf("nil Merge error = %v", err)
	}
	if first.Equal(second) || !first.Equal(first.Clone()) {
		t.Fatal("Equal returned the wrong result")
	}
	encoded, err := jsonv2.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	var decoded metadata.Extensions
	if err := jsonv2.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.Equal(first) {
		t.Fatalf("round trip = %s", encoded)
	}
}

func TestExtensionsValidateKeySegments(t *testing.T) {
	var extensions metadata.Extensions
	for _, key := range []string{"", "provider", "/name", "provider/", "provider/name/extra", "provider /name", "provider/na me"} {
		if err := extensions.Set(key, true); err == nil {
			t.Errorf("Set(%q) accepted invalid key", key)
		}
	}
	if err := extensions.Set("供应商.v2_0/选项-name1", true); err != nil {
		t.Fatalf("Set() rejected valid Unicode key: %v", err)
	}
	if err := extensions.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}
