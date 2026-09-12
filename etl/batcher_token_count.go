package etl

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/tokenizer"
)

// TokenCountBatcherConfig binds one tokenizer to hard per-document and
// per-batch token budgets.
type TokenCountBatcherConfig struct {
	// Counter is required.
	Counter tokenizer.TextCounter
	// MaxTokens is the required provider input limit. The batching layer has no
	// provider-neutral default because model limits differ.
	MaxTokens int
	// Reserve is the fraction of MaxTokens held back from each batch. Zero
	// means no reserve.
	Reserve float64
	// Formatter renders each document before counting. Nil uses document
	// text without metadata.
	Formatter Formatter
}

func (t TokenCountBatcherConfig) normalized() (TokenCountBatcherConfig, error) {
	if lo.IsNil(t.Counter) {
		return TokenCountBatcherConfig{}, errors.New("etl: token counter is required")
	}
	if t.MaxTokens <= 0 {
		return TokenCountBatcherConfig{}, errors.New("etl: maximum batch tokens must be positive")
	}
	if math.IsNaN(t.Reserve) || math.IsInf(t.Reserve, 0) || t.Reserve < 0 || t.Reserve >= 1 {
		return TokenCountBatcherConfig{}, errors.New("etl: token reserve must be in [0, 1)")
	}
	effective := t.MaxTokens
	if t.Reserve > 0 {
		usable := float64(t.MaxTokens) * (1 - t.Reserve)
		if usable >= float64(t.MaxTokens) {
			effective = t.MaxTokens - 1
		} else {
			effective = int(math.Floor(usable))
		}
	}
	if effective < 1 {
		return TokenCountBatcherConfig{}, errors.New("etl: token reserve leaves no usable batch budget")
	}
	t.MaxTokens = effective
	if t.Formatter == nil {
		t.Formatter = TextFormatter{}
	} else if lo.IsNil(t.Formatter) {
		return TokenCountBatcherConfig{}, errors.New("etl: formatter must not be a typed nil")
	}
	return t, nil
}

// TokenCountBatcher carves a document slice into batches that fit
// downstream embedding-service token limits. Document order is
// preserved across batches so callers can map embeddings back by
// position.
//
// A single document whose token count exceeds the per-batch budget is
// rejected with an error — the caller is expected to split it first
// (see [TokenSplitter]).
type TokenCountBatcher struct {
	counter   tokenizer.TextCounter
	maxTokens int
	formatter Formatter
}

type sizedDocument struct {
	document *document.Document
	tokens   int
}

// NewTokenCountBatcher validates budgets before any document reaches a load
// boundary.
func NewTokenCountBatcher(config TokenCountBatcherConfig) (*TokenCountBatcher, error) {
	config, err := config.normalized()
	if err != nil {
		return nil, err
	}

	return &TokenCountBatcher{
		counter:   config.Counter,
		maxTokens: config.MaxTokens,
		formatter: config.Formatter,
	}, nil
}

func (t *TokenCountBatcher) Batch(ctx context.Context, docs []*document.Document) ([][]*document.Document, error) {
	sized, err := t.measure(ctx, docs)
	if err != nil {
		return nil, err
	}
	return t.partition(sized), nil
}

func (t *TokenCountBatcher) measure(ctx context.Context, docs []*document.Document) ([]sizedDocument, error) {
	sized := make([]sizedDocument, 0, len(docs))
	for index, doc := range docs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if doc == nil {
			return nil, fmt.Errorf("etl: size document %d: %w", index, ErrNilDocument)
		}
		if err := doc.Validate(); err != nil {
			return nil, fmt.Errorf("etl: size document %d: %w", index, err)
		}
		rendered, err := t.formatter.Format(doc)
		if err != nil {
			return nil, fmt.Errorf("etl: format document %d for sizing: %w", index, err)
		}

		count, err := t.counter.CountText(ctx, rendered)
		if err != nil {
			return nil, fmt.Errorf("etl: count document %d tokens: %w", index, err)
		}
		if count < 0 {
			return nil, fmt.Errorf("etl: token counter returned %d for document %d", count, index)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if count > t.maxTokens {
			return nil, fmt.Errorf("etl: document %q has %d tokens, exceeding the batch budget of %d",
				doc.ID, count, t.maxTokens)
		}
		sized = append(sized, sizedDocument{document: doc, tokens: count})
	}
	return sized, nil
}

func (t *TokenCountBatcher) partition(sized []sizedDocument) [][]*document.Document {
	var (
		batches      [][]*document.Document
		currentBatch []*document.Document
		currentSum   int
	)

	for _, item := range sized {
		if item.tokens > t.maxTokens-currentSum {
			if len(currentBatch) > 0 {
				batches = append(batches, currentBatch)
			}
			currentBatch = nil
			currentSum = 0
		}
		currentBatch = append(currentBatch, item.document)
		currentSum += item.tokens
	}

	if len(currentBatch) > 0 {
		batches = append(batches, currentBatch)
	}
	return batches
}
