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
	data, provenance, err := r.source.Read(ctx, r.sourceBudget, r.sourceName)
	if err != nil {
		return nil, fmt.Errorf("json reader: read source: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	dec := jsontext.NewDecoder(bytes.NewReader(data))
	var docs []*document.Document
	if dec.PeekKind() == jsontext.KindBeginArray {
		docs, err = readElements(ctx, dec, provenance)
	} else {
		var doc *document.Document
		doc, err = readValue(dec, provenance)
		docs = []*document.Document{doc}
	}
	if err != nil {
		return nil, err
	}
	if _, err = dec.ReadToken(); !errors.Is(err, io.EOF) {
		return nil, errors.New("json reader: invalid trailing input")
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return docs, nil
}

// readElements reads a top-level array as one document per element. Each keeps
// its byte span and records its position among the source's elements.
func readElements(ctx context.Context, dec *jsontext.Decoder, provenance *document.Provenance) ([]*document.Document, error) {
	if _, err := dec.ReadToken(); err != nil {
		return nil, err
	}
	var docs []*document.Document
	for dec.PeekKind() != jsontext.KindEndArray {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		doc, err := readValue(dec, provenance)
		if err != nil {
			return nil, err
		}
		docs = append(docs, doc)
	}
	if _, err := dec.ReadToken(); err != nil {
		return nil, err
	}
	for index, doc := range docs {
		span := doc.Provenance
		doc.Provenance = provenance.Split("", index, len(docs))
		if span != nil {
			doc.Provenance.Bytes = span.Bytes
		}
	}
	return docs, nil
}

// readValue reads the next JSON value as a document spanning its source bytes.
func readValue(dec *jsontext.Decoder, provenance *document.Provenance) (*document.Document, error) {
	raw, err := dec.ReadValue()
	if err != nil {
		return nil, fmt.Errorf("json reader: decode value: %w", err)
	}
	doc, err := document.NewDocument(string(raw), nil)
	if err != nil {
		return nil, err
	}
	end := dec.InputOffset()
	doc.Provenance = origin.Span(provenance, end-int64(len(raw)), end)
	return doc, nil
}
