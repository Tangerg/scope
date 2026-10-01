package etl

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
)

type MetadataHeaderFormatterConfig struct {
	// ExcludedMetadata lists metadata keys omitted from rendered output.
	ExcludedMetadata []string
}

var _ Formatter = MetadataHeaderFormatter{}

// MetadataHeaderFormatter renders a [*document.Document] as
//
//	key1: value1
//	key2: value2
//
//	<document text>
type MetadataHeaderFormatter struct {
	excludedMetadata map[string]struct{}
}

// NewMetadataHeaderFormatter snapshots formatting policy into an immutable formatter.
func NewMetadataHeaderFormatter(config MetadataHeaderFormatterConfig) MetadataHeaderFormatter {
	return MetadataHeaderFormatter{excludedMetadata: lo.SliceToMap(config.ExcludedMetadata, func(key string) (string, struct{}) {
		return key, struct{}{}
	})}
}

// Format sorts metadata keys because map order would make rendered text, and
// therefore embeddings and token counts, nondeterministic. Without rendered
// metadata the output is the document text alone.
func (m MetadataHeaderFormatter) Format(doc *document.Document) (string, error) {
	if doc == nil {
		return "", ErrNilDocument
	}
	if err := doc.Validate(); err != nil {
		return "", fmt.Errorf("etl: format document: %w", err)
	}
	entries := make([]string, 0, len(doc.Metadata))
	for _, key := range slices.Sorted(maps.Keys(doc.Metadata)) {
		if _, excluded := m.excludedMetadata[key]; excluded {
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
