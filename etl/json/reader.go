// Package json reads JSON values into documents.
package json

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/etl"
	"github.com/Tangerg/scope/etl/internal/origin"
)

// Reader parses a JSON payload into [*document.Document] entries. Top-level
// arrays produce one document per element; every other JSON value produces one
// document containing its raw representation. Numbers retain their source
// spelling and are not restricted to the float64 range.
// Read admits one attempt on the sequential source. Reopen the input to
// retry after a failure; concurrent or later attempts return [etl.ErrSourceConsumed].
type Reader struct {
	source       *origin.Stream
	sourceName   string
	sourceBudget etl.SourceBudget
}

// ReaderConfig controls the whole-source memory budget. The zero budget uses
// [etl.DefaultMaxSourceBytes].
type ReaderConfig struct {
	// SourceName identifies the original input; empty means anonymous.
	SourceName   string
	SourceBudget etl.SourceBudget
}

func NewReader(source io.Reader, config ReaderConfig) (*Reader, error) {
	if lo.IsNil(source) {
		return nil, errors.New("json reader: source must not be nil")
	}
	if err := document.SourceName(config.SourceName).Validate(); err != nil {
		return nil, fmt.Errorf("json reader: %w", err)
	}
	return &Reader{source: origin.NewStream(source), sourceName: config.SourceName, sourceBudget: config.SourceBudget}, nil
}

func (r *Reader) Read(ctx context.Context) ([]*document.Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, p, err := r.source.Read(ctx, r.sourceBudget, r.sourceName)
	if err != nil {
		return nil, fmt.Errorf("json reader: read source: %w", err)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}

	dec := jsontext.NewDecoder(bytes.NewReader(data))
	array := dec.PeekKind() == '['
	if array {
		if _, err = dec.ReadToken(); err != nil {
			return nil, err
		}
	}
	var docs []*document.Document
	for !array || dec.PeekKind() != ']' {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		raw, readErr := dec.ReadValue()
		if readErr != nil {
			return nil, fmt.Errorf("json reader: decode value: %w", readErr)
		}
		doc, buildErr := document.NewDocument(string(raw), nil)
		if buildErr != nil {
			return nil, buildErr
		}
		end := dec.InputOffset()
		doc.Provenance = origin.Span(p, end-int64(len(raw)), end)
		docs = append(docs, doc)
		if !array {
			break
		}
	}
	if array {
		if _, err = dec.ReadToken(); err != nil {
			return nil, err
		}
	}
	if _, err = dec.ReadToken(); !errors.Is(err, io.EOF) {
		return nil, errors.New("json reader: invalid trailing input")
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if array {
		for i, d := range docs {
			if err = ctx.Err(); err != nil {
				return nil, err
			}
			span := d.Provenance
			d.Provenance = p.Split("", i, len(docs))
			if span != nil {
				d.Provenance.Bytes = span.Bytes
			}
		}
	}
	return docs, nil
}
