package weaviate

import (
	"errors"
	"testing"
)

func TestValidateObjectID(t *testing.T) {
	if err := validateObjectID("f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{
		"", "document-one", "42", "F9168C5E-CEB2-4FAA-B6BF-329BF39FA1E4",
		"f9168c5eceb24faab6bf329bf39fa1e4",
		"urn:uuid:f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4",
		"{f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4}",
	} {
		if err := validateObjectID(id); !errors.Is(err, ErrInvalidObjectID) {
			t.Fatalf("validateObjectID(%q) error = %v, want ErrInvalidObjectID", id, err)
		}
	}
}
