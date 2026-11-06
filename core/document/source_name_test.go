package document_test

import (
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/document"
)

func TestSourceNameAdmissionDoesNotRequireSourceBytes(t *testing.T) {
	for _, name := range []document.SourceName{"", "notes/guide", "资料/修订", " named "} {
		if err := name.Validate(); err != nil {
			t.Fatalf("source %q: %v", name, err)
		}
	}
	for _, name := range []document.SourceName{"\xff", " \t\n", "\u2003"} {
		if err := name.Validate(); !errors.Is(err, document.ErrInvalidDocument) {
			t.Fatalf("source %q: %v", name, err)
		}
		provenance := &document.Provenance{Source: string(name), Revision: "revision"}
		if err := provenance.Validate(); !errors.Is(err, document.ErrInvalidDocument) {
			t.Fatalf("provenance admitted source %q: %v", name, err)
		}
	}
}
