package etl

import (
	"context"
	"strings"

	"github.com/Tangerg/scope/core/document"
)

// TextSplitterConfig configures fixed-separator chunking. The zero Separator
// uses a newline.
type TextSplitterConfig struct {
	Separator string

	// IDGenerator, when set, assigns an ID to every emitted chunk.
	IDGenerator IDGenerator
}

// TextSplitter splits text on a fixed separator and enriches document chunks
// with the same lineage behavior as [Splitter].
type TextSplitter struct {
	separator string
	splitter  *Splitter
}

// NewTextSplitter treats the separator as a literal boundary and preserves the
// configured identity policy for emitted chunks.
func NewTextSplitter(config TextSplitterConfig) (*TextSplitter, error) {
	separator := config.Separator
	if separator == "" {
		separator = "\n"
	}
	splitter := &TextSplitter{separator: separator}
	base, err := NewSplitter(SplitterConfig{
		SplitFunc:   splitter.SplitText,
		IDGenerator: config.IDGenerator,
	})
	if err != nil {
		return nil, err
	}
	splitter.splitter = base
	return splitter, nil
}

func (t *TextSplitter) SplitText(ctx context.Context, text string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateTextEncoding(text); err != nil {
		return nil, err
	}
	return strings.Split(text, t.separator), nil
}

// Split emits document chunks with cloned metadata and lineage fields.
func (t *TextSplitter) Split(ctx context.Context, docs []*document.Document) ([]*document.Document, error) {
	return t.splitter.Split(ctx, docs)
}
