// Package index prepares and embeds source-bound documents before sending precomputed records
// to a native index. The encoder and database need not share a transaction.
package index

import (
	"context"
	"errors"
	"fmt"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/tokenizer"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/rag/vector"
)

// Destination checks provider-specific input constraints before model I/O. Upsert
// must still validate the current collection and the returned coordinates.
type Destination interface {
	vectorstore.Upserter
	CheckDocuments(context.Context, vectorstore.SpaceID, []*document.Document) error
}

type WriterConfig struct {
	Destination Destination
	Encoder     *vector.Encoder
	Formatter   document.Formatter
	Batching    tokenizer.BatchConfig
}

type Writer struct{ config WriterConfig }

func NewWriter(config WriterConfig) (*Writer, error) {
	if lo.IsNil(config.Destination) || config.Encoder == nil {
		return nil, errors.New("rag/index: destination and encoder are required")
	}
	if err := config.Encoder.Space().Validate(); err != nil {
		return nil, err
	}
	if config.Formatter != nil && lo.IsNil(config.Formatter) {
		return nil, errors.New("rag/index: nil formatter")
	}
	if err := config.Batching.Validate(); err != nil {
		return nil, err
	}
	return &Writer{config: config}, nil
}

func (w *Writer) Write(ctx context.Context, documents []*document.Document) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := vectorstore.ValidateDocuments(documents); err != nil {
		return err
	}
	docs := make([]*document.Document, len(documents))
	for i, doc := range documents {
		if sourceErr := doc.Provenance.RequireRevision(); sourceErr != nil {
			return fmt.Errorf("rag/index: document %d: %w", i, sourceErr)
		}
		docs[i] = doc.Clone()
	}
	// Admission inspects a projection; it does not own the records subsequently encoded.
	admission := make([]*document.Document, len(docs))
	for i, doc := range docs {
		admission[i] = doc.Clone()
	}
	if err := w.config.Destination.CheckDocuments(ctx, w.config.Encoder.Space(), admission); err != nil {
		return err
	}
	var err error
	texts := make([]string, len(docs))
	for i, doc := range docs {
		if cancelErr := ctx.Err(); cancelErr != nil {
			return cancelErr
		}
		texts[i] = doc.Text
		if w.config.Formatter != nil {
			// A formatter owns its working copy, never the record whose identity
			// and source have already passed destination admission.
			texts[i], err = w.config.Formatter.Format(doc.Clone())
			if err != nil {
				return fmt.Errorf("rag/index: format document %d: %w", i, err)
			}
		}
		if _, inputErr := embedding.NewRequest([]string{texts[i]}); inputErr != nil {
			return inputErr
		}
	}
	groups, err := w.config.Batching.Batches(ctx, texts)
	if err != nil {
		return err
	}
	batch := &vectorstore.Batch{Space: w.config.Encoder.Space(), Records: make([]vectorstore.Record, len(docs))}
	offset := 0
	for _, group := range groups {
		values, err := w.config.Encoder.EncodeDocuments(ctx, group)
		if err != nil {
			return err
		}
		for j, coords := range values {
			batch.Records[offset+j] = vectorstore.Record{Document: docs[offset+j], Values: coords}
		}
		offset += len(group)
	}
	if err := batch.ValidateFor(batch.Space); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return w.config.Destination.Upsert(ctx, batch)
}
