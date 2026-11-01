// Package origin binds each reader's bounded input snapshot to its original
// revision. Parsers, normalizers and splitters never hash rendered text.
package origin

import (
	"context"
	"io"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/etl"
)

func Read(ctx context.Context, r io.Reader, budget etl.SourceBudget, name string) ([]byte, *document.Provenance, error) {
	raw, err := budget.ReadAll(ctx, r)
	if err != nil {
		return nil, nil, err
	}
	p := document.NewProvenance(name, raw)
	if err = p.Validate(); err != nil {
		return nil, nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, nil, err
	}
	return raw, p, nil
}

// Span identifies bytes only when the parser can prove both original offsets.
// An anonymous single document need not acquire a fabricated source identity.
func Span(p *document.Provenance, start, end int64) *document.Provenance {
	if p == nil {
		return nil
	}
	result := p.Clone()
	result.Bytes = &document.ByteRange{Start: start, End: end}
	return result
}

// Parts numbers only documents that were actually emitted. Parser output is
// generally not an exact byte slice, so Split discards any inherited byte span.
func Parts(ctx context.Context, p *document.Provenance, docs []*document.Document) ([]*document.Document, error) {
	for i, d := range docs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		d.Provenance = p.Split("", i, len(docs))
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return docs, nil
}
