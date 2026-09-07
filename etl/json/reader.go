// Package json reads JSON values into documents.
package json

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/etl"
)

// Reader parses a JSON payload into [*document.Document] entries. Top-level
// arrays produce one document per element; every other JSON value produces one
// document containing its raw representation. Numbers retain their source
// spelling and are not restricted to the float64 range.
//
// Use it to ingest API responses, dump files, or seed fixture data.
//
// Example:
//
//	r, err := json.NewReader(strings.NewReader(`[{"id":1},{"id":2}]`), json.ReaderConfig{})
//	docs, err := r.Read(ctx) // 2 documents
type Reader struct {
	source       io.Reader
	sourceBudget etl.SourceBudget
}

// ReaderConfig controls the whole-source memory budget. The zero budget uses
// [etl.DefaultMaxSourceBytes].
type ReaderConfig struct {
	SourceBudget etl.SourceBudget
}

// NewReader validates the typed JSON projection and source budget before any
// input is consumed.
func NewReader(source io.Reader, config ReaderConfig) (*Reader, error) {
	if lo.IsNil(source) {
		return nil, errors.New("json reader: source must not be nil")
	}
	return &Reader{source: source, sourceBudget: config.SourceBudget}, nil
}

// Read consumes the source and converts its top-level value to documents.
func (r *Reader) Read(ctx context.Context) ([]*document.Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := r.sourceBudget.ReadAll(ctx, r.source)
	if err != nil {
		return nil, fmt.Errorf("json reader: read source: %w", err)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}

	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		return r.parseArray(ctx, trimmed)
	}
	var value json.RawMessage
	if unmarshalErr := json.Unmarshal(trimmed, &value); unmarshalErr != nil {
		return nil, fmt.Errorf("json reader: decode source: %w", unmarshalErr)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}

	doc, err := document.NewDocument(string(value), nil)
	if err != nil {
		return nil, fmt.Errorf("json reader: build document: %w", err)
	}
	return []*document.Document{doc}, nil
}

func (*Reader) parseArray(ctx context.Context, data []byte) ([]*document.Document, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("json reader: decode array: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	docs := make([]*document.Document, 0, len(items))
	for index, item := range items {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		doc, err := document.NewDocument(string(item), nil)
		if err != nil {
			return nil, fmt.Errorf("json reader: build array document %d: %w", index, err)
		}
		docs = append(docs, doc)
	}
	return docs, nil
}
