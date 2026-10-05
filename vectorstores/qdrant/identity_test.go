package qdrant

import (
	"errors"
	"testing"

	qdrantclient "github.com/qdrant/go-client/qdrant"
)

func TestPointIDRoundTrip(t *testing.T) {
	for _, id := range []string{"0", "42", "18446744073709551615", "f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4"} {
		pointID, err := parsePointID(id)
		if err != nil {
			t.Fatalf("parsePointID(%q): %v", id, err)
		}
		got, err := formatPointID(pointID)
		if err != nil {
			t.Fatalf("formatPointID(parsePointID(%q)): %v", id, err)
		}
		if got != id {
			t.Fatalf("formatPointID(parsePointID(%q)) = %q", id, got)
		}
	}
}

func TestFormatPointIDRejectsMissingVariant(t *testing.T) {
	for _, id := range []*qdrantclient.PointId{nil, {}} {
		if _, err := formatPointID(id); !errors.Is(err, ErrInvalidPointID) {
			t.Fatalf("formatPointID(%v) error = %v, want ErrInvalidPointID", id, err)
		}
	}
}

func TestPointIDRejectsNonCanonicalValues(t *testing.T) {
	for _, id := range []string{
		"", "01", "document-one", "18446744073709551616",
		"F9168C5E-CEB2-4FAA-B6BF-329BF39FA1E4",
		"f9168c5eceb24faab6bf329bf39fa1e4",
		"urn:uuid:f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4",
		"{f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4}",
	} {
		if _, err := parsePointID(id); !errors.Is(err, ErrInvalidPointID) {
			t.Fatalf("parsePointID(%q) error = %v, want ErrInvalidPointID", id, err)
		}
	}
}

func TestFormatPointIDRejectsNonCanonicalUUIDs(t *testing.T) {
	for _, id := range []string{
		"F9168C5E-CEB2-4FAA-B6BF-329BF39FA1E4",
		"f9168c5eceb24faab6bf329bf39fa1e4",
		"urn:uuid:f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4",
		"{f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4}",
		"42",
	} {
		if _, err := formatPointID(qdrantclient.NewIDUUID(id)); !errors.Is(err, ErrInvalidPointID) {
			t.Fatalf("formatPointID(UUID %q) error = %v, want ErrInvalidPointID", id, err)
		}
	}
}
