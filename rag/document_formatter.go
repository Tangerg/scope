package rag

import (
	"errors"
	"fmt"

	"github.com/Tangerg/scope/core/document"
)

// ErrUnsupportedMedia means a formatter cannot represent document media.
var ErrUnsupportedMedia = errors.New("rag: document formatter does not support media")

// DocumentFormatter renders one retrieved document for model input.
type DocumentFormatter interface {
	// Format renders one valid document without mutating or retaining it. The
	// returned text is inserted into model context, so implementations must be
	// deterministic for the same document and return an error for unsupported
	// media rather than silently dropping evidence.
	Format(doc *document.Document) (string, error)
}

// DocumentFormatterFunc adapts a pure document projection to DocumentFormatter.
type DocumentFormatterFunc func(*document.Document) (string, error)

func (d DocumentFormatterFunc) Format(doc *document.Document) (string, error) {
	return d(doc)
}

// TextFormatter renders document text and rejects media that text alone cannot
// represent. Its zero value is ready to use.
type TextFormatter struct{}

var _ DocumentFormatter = TextFormatter{}

func (TextFormatter) Format(doc *document.Document) (string, error) {
	if err := doc.Validate(); err != nil {
		return "", fmt.Errorf("rag: format document: %w", err)
	}
	if doc.Media != nil {
		return "", ErrUnsupportedMedia
	}
	return doc.Text, nil
}
