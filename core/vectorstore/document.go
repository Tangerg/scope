package vectorstore

import (
	"fmt"
	"strings"
	"unicode/utf8"

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
	if err := ValidateDocumentID(doc.ID); err != nil {
		return err
	}
	if err := document.ValidateStoredMetadata(doc.Metadata); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidDocument, err)
	}
	return nil
}

// ValidateDocumentID checks the identity every stored document carries:
// nonblank, valid UTF-8 text. Stores apply their native key rules after it.
func ValidateDocumentID(id string) error {
	if strings.TrimSpace(id) == "" {
		return ErrMissingDocumentID
	}
	if !utf8.ValidString(id) {
		return fmt.Errorf("%w: ID is not valid UTF-8", ErrInvalidDocument)
	}
	return nil
}

// DistinctIDs validates every identity before any native call and returns
// the IDs without repeats, in first-occurrence order.
func DistinctIDs(ids []string) ([]string, error) {
	distinct := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for index, id := range ids {
		if err := ValidateDocumentID(id); err != nil {
			return nil, fmt.Errorf("%w: ids[%d]: %w", ErrInvalidRequest, index, err)
		}
		if _, repeated := seen[id]; !repeated {
			seen[id] = struct{}{}
			distinct = append(distinct, id)
		}
	}
	return distinct, nil
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
