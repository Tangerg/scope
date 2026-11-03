package document

import (
	"fmt"

	"github.com/Tangerg/scope/core/metadata"
)

// StoredProvenanceKey is reserved in the physical JSON metadata cell. It is
// never part of the searchable business metadata returned by a store.
const StoredProvenanceKey = "_scope_document_provenance"

type storedOrigin struct {
	Provenance *Provenance `json:"provenance"`
	// An origin requires an object on the wire even when business metadata is null.
	NullMetadata bool `json:"null_metadata,omitempty"`
}

// ValidateStoredMetadata rejects business fields that would collide with
// reserved data in a shared physical metadata cell.
func ValidateStoredMetadata(facts metadata.Map) error {
	if _, reserved := facts[StoredProvenanceKey]; reserved {
		return fmt.Errorf("%w: reserved physical metadata key %q", ErrInvalidDocument, StoredProvenanceKey)
	}
	return nil
}
