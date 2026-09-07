package etl

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
)

// SimpleFormatterConfig controls the stable textual projection used before
// splitting or indexing.
type SimpleFormatterConfig struct {
	// ExcludedMetadata lists metadata keys omitted from rendered output.
	ExcludedMetadata []string
}

var _ Formatter = SimpleFormatter{}

// SimpleFormatter renders a [*document.Document] as
//
//	key1: value1
//	key2: value2
//
//	<document text>
//
// Metadata keys can be excluded when constructing the formatter. Use
// independently configured formatters when different consumers need different
// representations.
//
// Example:
//
//	f := etl.NewSimpleFormatter(etl.SimpleFormatterConfig{
//	    ExcludedMetadata: []string{"row_id", "internal"},
//	})
type SimpleFormatter struct {
	excludedMetadata map[string]struct{}
}

// NewSimpleFormatter snapshots formatting policy into an immutable formatter.
func NewSimpleFormatter(config SimpleFormatterConfig) SimpleFormatter {
	return SimpleFormatter{excludedMetadata: lo.SliceToMap(config.ExcludedMetadata, func(key string) (string, struct{}) {
		return key, struct{}{}
	})}
}

// Format renders doc by emitting filtered metadata as `key: value` lines
// (sorted by key — map iteration order would make the rendered text,
// and thus embedding inputs and token counts, non-deterministic)
// followed by a blank line and the document text. With no metadata
// (filtered empty), the output is just doc.Text — no leading newlines.
func (s SimpleFormatter) Format(doc *document.Document) (string, error) {
	if doc == nil {
		return "", ErrNilDocument
	}
	if err := doc.Validate(); err != nil {
		return "", fmt.Errorf("etl: format document: %w", err)
	}
	entries := make([]string, 0, len(doc.Metadata))
	for _, key := range slices.Sorted(maps.Keys(doc.Metadata)) {
		if _, excluded := s.excludedMetadata[key]; excluded {
			continue
		}
		value, err := metadataValue(doc.Metadata[key]).text()
		if err != nil {
			return "", fmt.Errorf("etl: format metadata %q: %w", key, err)
		}
		entries = append(entries, key+": "+value)
	}
	if len(entries) == 0 {
		return doc.Text, nil
	}
	return strings.Join(entries, "\n") + "\n\n" + doc.Text, nil
}
