package vectorstore

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
)

// IndexRequest describes documents for one indexing call. It owns their
// provider-independent validation and batching lifecycle. Validation covers
// the complete request; it does not make backend writes atomic across batches.
type IndexRequest struct {
	Documents []*document.Document `json:"documents"`
}

// NewIndexRequest validates every document up front. Stores validate again at
// entry because callers can modify the request or its documents.
func NewIndexRequest(documents []*document.Document) (*IndexRequest, error) {
	request := &IndexRequest{Documents: slices.Clone(documents)}
	if err := request.Validate(); err != nil {
		return nil, fmt.Errorf("vectorstore: create index request: %w", err)
	}
	return request, nil
}

func (i *IndexRequest) Validate() error {
	if i == nil {
		return fmt.Errorf("%w: index request is nil", ErrInvalidRequest)
	}
	if len(i.Documents) == 0 {
		return fmt.Errorf("%w: %w", ErrInvalidRequest, ErrEmptyDocuments)
	}

	seen := make(map[string]int, len(i.Documents))
	for index, indexedDocument := range i.Documents {
		if indexedDocument == nil {
			return fmt.Errorf("%w: %w: documents[%d] is nil", ErrInvalidRequest, ErrInvalidDocument, index)
		}
		if err := indexedDocument.Validate(); err != nil {
			return fmt.Errorf("%w: %w: documents[%d]: %w", ErrInvalidRequest, ErrInvalidDocument, index, err)
		}
		if strings.TrimSpace(indexedDocument.ID) == "" {
			return fmt.Errorf("%w: %w: documents[%d]", ErrInvalidRequest, ErrMissingDocumentID, index)
		}
		if indexedDocument.Text == "" {
			return fmt.Errorf("%w: %w: documents[%d] has no text to embed", ErrInvalidRequest, ErrInvalidDocument, index)
		}
		if first, duplicate := seen[indexedDocument.ID]; duplicate {
			return fmt.Errorf("%w: %w %q at documents[%d] and documents[%d]",
				ErrInvalidRequest, ErrDuplicateDocumentID, indexedDocument.ID, first, index)
		}
		seen[indexedDocument.ID] = index
	}
	return nil
}

// Texts returns an owned, order-preserving projection of document text.
func (i *IndexRequest) Texts() ([]string, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	texts := make([]string, len(i.Documents))
	for index, indexedDocument := range i.Documents {
		texts[index] = indexedDocument.Text
	}
	return texts, nil
}

func (i IndexRequest) MarshalJSON() ([]byte, error) {
	if err := (&i).Validate(); err != nil {
		return nil, err
	}
	type wireIndexRequest IndexRequest
	return json.Marshal(wireIndexRequest(i))
}

func (i *IndexRequest) UnmarshalJSON(data []byte) error {
	if i == nil {
		return fmt.Errorf("%w: index request receiver is nil", ErrInvalidRequest)
	}
	type wireIndexRequest IndexRequest
	var decoded wireIndexRequest
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode index request: %w", ErrInvalidRequest, err)
	}
	candidate := IndexRequest(decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*i = candidate
	return nil
}

// Batch delegates to batcher and returns validated, order-preserving child
// requests. The receiver itself must be valid.
func (i *IndexRequest) Batch(ctx context.Context, batcher Batcher) ([]*IndexRequest, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	if lo.IsNil(batcher) {
		return nil, errors.New("vectorstore: batch index request: batcher must not be nil")
	}

	documentBatches, err := batcher.Batch(ctx, i.Documents)
	if err != nil {
		return nil, err
	}
	if err := i.validateBatches(documentBatches); err != nil {
		return nil, err
	}

	batches := make([]*IndexRequest, len(documentBatches))
	for index, documents := range documentBatches {
		batch, err := NewIndexRequest(documents)
		if err != nil {
			return nil, fmt.Errorf("vectorstore: build index batch %d: %w", index, err)
		}
		batches[index] = batch
	}
	return batches, nil
}

func (i *IndexRequest) validateBatches(batches [][]*document.Document) error {
	next := 0
	for batchIndex, batch := range batches {
		if len(batch) == 0 {
			return fmt.Errorf("%w: batch %d is empty", ErrInvalidBatcherOutput, batchIndex)
		}
		for documentIndex, indexedDocument := range batch {
			if next >= len(i.Documents) {
				return fmt.Errorf("%w: unexpected document at batch %d index %d",
					ErrInvalidBatcherOutput, batchIndex, documentIndex)
			}
			if indexedDocument != i.Documents[next] {
				return fmt.Errorf("%w: document at batch %d index %d does not match input index %d",
					ErrInvalidBatcherOutput, batchIndex, documentIndex, next)
			}
			next++
		}
	}
	if next != len(i.Documents) {
		return fmt.Errorf("%w: returned %d of %d documents", ErrInvalidBatcherOutput, next, len(i.Documents))
	}
	return nil
}

// Indexer embeds and indexes documents in the vector store. The store runs:
//
//  1. Embedding (text → vector)
//  2. Indexing (vector + metadata → searchable record)
//  3. Storage (record → durable backend)
type Indexer interface {
	// Index persists request documents using caller-assigned IDs. Existing IDs
	// are replaced according to the backend's upsert semantics. Implementations
	// validate the complete request before external I/O. Index does not promise
	// atomic writes across batches: an error may leave earlier documents stored.
	// Backend-specific transaction guarantees belong to the implementation.
	// Accepted document content, including Media, must survive retrieval;
	// implementations reject unsupported media before external I/O.
	//
	// Index never invents document IDs: its error-only result has no channel for
	// returning generated identities to the caller.
	Index(ctx context.Context, request *IndexRequest) error
}
