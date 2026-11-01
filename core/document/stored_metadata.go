package document

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"maps"

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

// ValidateStorageMetadata rejects business fields that would collide with
// reserved data in a shared physical metadata cell.
func ValidateStorageMetadata(facts metadata.Map) error {
	if _, reserved := facts[StoredProvenanceKey]; reserved {
		return fmt.Errorf("%w: reserved physical metadata key %q", ErrInvalidDocument, StoredProvenanceKey)
	}
	return nil
}

// MarshalStoredMetadata encodes the document's business facts and optional
// provenance in one native JSON cell. Native filter projections must still
// be built from doc.Metadata, never by indexing this physical representation.
func MarshalStoredMetadata(doc *Document) ([]byte, error) {
	if doc == nil {
		return nil, ErrInvalidDocument
	}
	if err := ValidateStorageMetadata(doc.Metadata); err != nil {
		return nil, err
	}
	if doc.Provenance == nil {
		return doc.Metadata.MarshalJSON()
	}
	if err := doc.Provenance.Validate(); err != nil {
		return nil, err
	}
	cell := maps.Clone(doc.Metadata)
	if err := cell.Set(StoredProvenanceKey, storedOrigin{doc.Provenance, doc.Metadata == nil}); err != nil {
		return nil, err
	}
	return cell.MarshalJSON()
}

// UnmarshalStoredMetadata restores both fields without changing doc's identity
// or content. Invalid native data leaves the destination untouched. The raw cell
// may separately be retained as the backend's conditional-write token.
func UnmarshalStoredMetadata(data []byte, doc *Document) error {
	if doc == nil {
		return ErrInvalidDocument
	}
	var facts metadata.Map
	if err := facts.UnmarshalJSON(data); err != nil {
		return err
	}
	var origin storedOrigin
	if raw, present := facts[StoredProvenanceKey]; present {
		if err := jsonv2.Unmarshal(raw, &origin, jsonv2.RejectUnknownMembers(true)); err != nil {
			return fmt.Errorf("%w: invalid stored provenance: %w", ErrInvalidDocument, err)
		}
		if origin.Provenance == nil {
			return fmt.Errorf("%w: absent stored provenance", ErrInvalidDocument)
		}
		if err := origin.Provenance.Validate(); err != nil {
			return fmt.Errorf("%w: stored provenance: %w", ErrInvalidDocument, err)
		}
		delete(facts, StoredProvenanceKey)
		if origin.NullMetadata {
			if len(facts) > 0 {
				return fmt.Errorf("%w: fields contradict null business metadata", ErrInvalidDocument)
			}
			facts = nil
		}
	}
	doc.Metadata, doc.Provenance = facts, origin.Provenance
	return nil
}
