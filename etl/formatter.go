package etl

import (
	"fmt"

	"github.com/Tangerg/scope/core/document"
)

// Formatter renders a document under one frozen policy; consumers needing
// different representations configure separate formatters.
type Formatter interface {
	// Format renders one valid document under the receiver's frozen policy. It
	// must not mutate or retain the document and must be deterministic so the
	// same pipeline input produces stable downstream chunks and identities.
	Format(document *document.Document) (string, error)
}

type FormatterFunc func(*document.Document) (string, error)

func (f FormatterFunc) Format(doc *document.Document) (string, error) {
	return f(doc)
}

// TextFormatter renders only document text. Its zero value is ready to use.
type TextFormatter struct{}

func (TextFormatter) Format(doc *document.Document) (string, error) {
	if doc == nil {
		return "", ErrNilDocument
	}
	if err := doc.Validate(); err != nil {
		return "", fmt.Errorf("etl: format document: %w", err)
	}
	return doc.Text, nil
}
