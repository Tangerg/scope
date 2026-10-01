package document

import (
	"errors"
	"fmt"
)

// ErrUnsupportedMedia reports a document whose media a text formatter cannot
// represent. Formatters return it instead of dropping the media silently.
var ErrUnsupportedMedia = errors.New("document: formatter does not support media")

// Formatter renders a document as text under one frozen policy; consumers
// needing different representations configure separate formatters.
type Formatter interface {
	// Format renders one valid document without mutating or retaining it. It
	// must be deterministic, because the text feeds chunk identities,
	// embeddings, and model context. Media the policy cannot represent returns
	// an error rather than being dropped.
	Format(doc *Document) (string, error)
}

type FormatterFunc func(*Document) (string, error)

func (f FormatterFunc) Format(doc *Document) (string, error) { return f(doc) }

// TextFormatter renders the document text alone and rejects media with
// ErrUnsupportedMedia. Its zero value is ready to use.
type TextFormatter struct{}

var _ Formatter = TextFormatter{}

func (TextFormatter) Format(doc *Document) (string, error) {
	if err := doc.Validate(); err != nil {
		return "", fmt.Errorf("document: format: %w", err)
	}
	if doc.Media != nil {
		return "", ErrUnsupportedMedia
	}
	return doc.Text, nil
}
