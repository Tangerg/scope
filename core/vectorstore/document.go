package vectorstore

import (
	"fmt"
	"strings"

	"github.com/Tangerg/scope/core/document"
)

// ValidateDocument checks stored identity and content. A prepared vector may describe
// media without text; individual stores can reject media they cannot preserve.
func ValidateDocument(doc *document.Document) error {
	if doc == nil {
		return fmt.Errorf("%w: missing document", ErrInvalidDocument)
	}
	return validatePresentDocument(doc)
}
func validatePresentDocument(doc *document.Document) error {
	if err := doc.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidDocument, err)
	}
	if strings.TrimSpace(doc.ID) == "" {
		return ErrMissingDocumentID
	}
	if err := document.ValidateStoredMetadata(doc.Metadata); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidDocument, err)
	}
	return nil
}

// ValidateDocuments additionally rejects empty input and duplicate identities.
func ValidateDocuments(docs []*document.Document) error {
	if len(docs) == 0 {
		return fmt.Errorf("%w: %w", ErrInvalidRequest, ErrEmptyDocuments)
	}
	seen := make(map[string]int, len(docs))
	for i, d := range docs {
		if err := ValidateDocument(d); err != nil {
			return fmt.Errorf("%w: documents[%d]: %w", ErrInvalidRequest, i, err)
		}
		if err := uniqueID(seen, d.ID, i); err != nil {
			return err
		}
	}
	return nil
}
func uniqueID(seen map[string]int, id string, i int) error {
	if first, ok := seen[id]; ok {
		return fmt.Errorf("%w: %w %q at positions %d and %d", ErrInvalidRequest, ErrDuplicateDocumentID, id, first, i)
	}
	seen[id] = i
	return nil
}
