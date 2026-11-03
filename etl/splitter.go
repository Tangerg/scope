package etl

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
)

var (
	ErrInvalidTextEncoding      = errors.New("etl: invalid text encoding")
	ErrUnsupportedDocumentMedia = errors.New("etl: text splitting does not support document media")
)

// TextSplitter is the atomic policy a Splitter applies to each document's
// text: it owns where chunks break, while Splitter owns metadata and lineage.
type TextSplitter interface {
	SplitText(ctx context.Context, text string) ([]string, error)
}

type TextSplitterFunc func(ctx context.Context, text string) ([]string, error)

func (t TextSplitterFunc) SplitText(ctx context.Context, text string) ([]string, error) {
	return t(ctx, text)
}

type SplitterConfig struct {
	// TextSplitter is required.
	TextSplitter TextSplitter

	// IDGenerator, when set, assigns an ID to each emitted chunk.
	IDGenerator IDGenerator
}

// Splitter applies a TextSplitter to documents, clones source metadata onto
// every chunk, and records chunk lineage. Documents containing media are
// rejected because a text policy cannot assign media to chunks.
type Splitter struct {
	textSplitter TextSplitter
	idGenerator  IDGenerator
}

func NewSplitter(config SplitterConfig) (*Splitter, error) {
	if lo.IsNil(config.TextSplitter) {
		return nil, errors.New("etl: text splitter is required")
	}
	if config.IDGenerator != nil && lo.IsNil(config.IDGenerator) {
		return nil, errors.New("etl: ID generator must not be a typed nil")
	}
	return &Splitter{
		textSplitter: config.TextSplitter,
		idGenerator:  config.IDGenerator,
	}, nil
}

func (s *Splitter) splitText(ctx context.Context, text string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateTextEncoding(text); err != nil {
		return nil, err
	}
	chunks, err := s.textSplitter.SplitText(ctx, text)
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

// Split preserves input order and per-document chunk order.
func (s *Splitter) Split(ctx context.Context, docs []*document.Document) ([]*document.Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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
	if doc.Media != nil {
		return nil, ErrUnsupportedDocumentMedia
	}
	chunks, err := s.splitText(ctx, doc.Text)
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
		chunk, err := s.newChunk(ctx, doc, text, index, total)
		if err != nil {
			return nil, err
		}
		out = append(out, chunk)
	}
	return out, nil
}

func (s *Splitter) newChunk(ctx context.Context, source *document.Document, text string, index, total int) (*document.Document, error) {
	chunk, err := document.NewDocument(text, nil)
	if err != nil {
		return nil, err
	}
	chunk.Metadata = source.Metadata.Clone()
	chunk.Provenance = source.Provenance.Split(source.ID, index, total)
	if err := assignID(ctx, chunk, s.idGenerator); err != nil {
		return nil, err
	}
	return chunk, nil
}
