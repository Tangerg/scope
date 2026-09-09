package etl

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
)

var (
	// ErrInvalidTextEncoding identifies document text that cannot be split as
	// valid UTF-8.
	ErrInvalidTextEncoding = errors.New("etl: invalid text encoding")
	// ErrDocumentHasNoText rejects chunking when only media content is present.
	ErrDocumentHasNoText = errors.New("etl: document has no text to split")
)

// Chunk-lineage metadata keys stamped by [Splitter] on every emitted chunk.
const (
	// MetadataKeyParentID holds the source document's ID. It is omitted when
	// the source document has no ID.
	MetadataKeyParentID = "parent_document_id"

	// MetadataKeyChunkIndex holds the zero-based position among emitted chunks.
	MetadataKeyChunkIndex = "chunk_index"

	// MetadataKeyChunkTotal holds the number of chunks emitted for the source.
	MetadataKeyChunkTotal = "chunk_total"
)

// SplitterConfig separates chunking policy from optional identity assignment.
type SplitterConfig struct {
	// SplitFunc is required and owns the text splitting policy.
	SplitFunc func(context.Context, string) ([]string, error)

	// IDGenerator, when set, assigns an ID to each emitted chunk.
	IDGenerator IDGenerator
}

// Splitter applies a text splitting policy to documents, clones source
// metadata onto every chunk, and records chunk lineage.
type Splitter struct {
	splitFunc   func(context.Context, string) ([]string, error)
	idGenerator IDGenerator
}

// NewSplitter rejects missing policy and snapshots the optional ID boundary.
func NewSplitter(config SplitterConfig) (*Splitter, error) {
	if config.SplitFunc == nil {
		return nil, errors.New("etl: split function is required")
	}
	if config.IDGenerator != nil && lo.IsNil(config.IDGenerator) {
		return nil, errors.New("etl: ID generator must not be a typed nil")
	}
	return &Splitter{
		splitFunc:   config.SplitFunc,
		idGenerator: config.IDGenerator,
	}, nil
}

// SplitText applies the configured text splitting policy directly.
func (s *Splitter) SplitText(ctx context.Context, text string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateTextEncoding(text); err != nil {
		return nil, err
	}
	chunks, err := s.splitFunc(ctx, text)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return chunks, nil
}

func validateTextEncoding(text string) error {
	if !utf8.ValidString(text) {
		return fmt.Errorf("%w: text is not valid UTF-8", ErrInvalidTextEncoding)
	}
	return nil
}

// Split emits chunks for every input document. Input order and per-document
// chunk order are preserved.
func (s *Splitter) Split(ctx context.Context, docs []*document.Document) ([]*document.Document, error) {
	out := make([]*document.Document, 0, len(docs))
	for index, doc := range docs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if doc == nil {
			return nil, fmt.Errorf("etl: split document %d: %w", index, ErrNilDocument)
		}
		if err := doc.Validate(); err != nil {
			return nil, fmt.Errorf("etl: split document %d: %w", index, err)
		}
		chunks, err := s.splitDocument(ctx, doc)
		if err != nil {
			return nil, fmt.Errorf("etl: split document %d: %w", index, err)
		}
		out = append(out, chunks...)
	}
	return out, nil
}

func (s *Splitter) splitDocument(ctx context.Context, doc *document.Document) ([]*document.Document, error) {
	if doc.Text == "" {
		return nil, ErrDocumentHasNoText
	}
	chunks, err := s.SplitText(ctx, doc.Text)
	if err != nil {
		return nil, err
	}

	nonEmpty := make([]string, 0, len(chunks))
	for _, text := range chunks {
		if strings.TrimSpace(text) != "" {
			nonEmpty = append(nonEmpty, text)
		}
	}

	total := len(nonEmpty)
	out := make([]*document.Document, 0, total)
	for index, text := range nonEmpty {
		chunk, err := document.NewDocument(text, nil)
		if err != nil {
			return nil, err
		}

		chunk.Metadata = doc.Metadata.Clone()
		if chunk.Metadata == nil {
			chunk.Metadata = metadata.Map{}
		}
		// Inherited lineage describes an earlier split, not this source.
		delete(chunk.Metadata, MetadataKeyParentID)
		if err := chunk.Metadata.Set(MetadataKeyChunkIndex, index); err != nil {
			return nil, err
		}
		if err := chunk.Metadata.Set(MetadataKeyChunkTotal, total); err != nil {
			return nil, err
		}
		if doc.ID != "" {
			if err := chunk.Metadata.Set(MetadataKeyParentID, doc.ID); err != nil {
				return nil, err
			}
		}
		if err := assignID(ctx, chunk, s.idGenerator); err != nil {
			return nil, err
		}
		out = append(out, chunk)
	}
	return out, nil
}
