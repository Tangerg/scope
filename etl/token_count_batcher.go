package etl

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/tokenizer"
)

type TokenCountBatcherConfig struct {
	Counter tokenizer.TextCounter
	// MaxTokens is the required provider input limit. The batching layer has no
	// provider-neutral default because model limits differ.
	MaxTokens int
	// Reserve is the fraction of MaxTokens held back from each batch. Zero
	// means no reserve.
	Reserve float64
	// Formatter renders each document before counting. Nil counts the
	// document text an index embeds; media contributes no text tokens.
	Formatter document.Formatter
}

func (t TokenCountBatcherConfig) normalize() (TokenCountBatcherConfig, error) {
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
	if t.Formatter != nil && lo.IsNil(t.Formatter) {
		return TokenCountBatcherConfig{}, errors.New("etl: formatter must not be a typed nil")
	}
	return t, nil
}

// TokenCountBatcher preserves document order and rejects any document that
// exceeds the batch token budget. Split oversized documents before batching.
type TokenCountBatcher struct {
	batching  tokenizer.BatchConfig
	formatter document.Formatter
}

func NewTokenCountBatcher(config TokenCountBatcherConfig) (*TokenCountBatcher, error) {
	config, err := config.normalize()
	if err != nil {
		return nil, err
	}

	return &TokenCountBatcher{
		batching:  tokenizer.BatchConfig{Counter: config.Counter, MaxTokens: config.MaxTokens},
		formatter: config.Formatter,
	}, nil
}

func (t *TokenCountBatcher) Batch(ctx context.Context, docs []*document.Document) ([][]*document.Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	texts := make([]string, len(docs))
	for i, doc := range docs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if doc == nil {
			return nil, fmt.Errorf("etl: size document %d: %w", i, ErrNilDocument)
		}
		if err := doc.Validate(); err != nil {
			return nil, fmt.Errorf("etl: size document %d: %w", i, err)
		}
		texts[i] = doc.Text
		if t.formatter != nil {
			v, err := t.formatter.Format(doc)
			if err != nil {
				return nil, fmt.Errorf("etl: format document %d: %w", i, err)
			}
			texts[i] = v
		}
	}
	groups, err := t.batching.Batches(ctx, texts)
	if err != nil {
		return nil, err
	}
	var batches [][]*document.Document
	offset := 0
	for _, g := range groups {
		end := offset + len(g)
		batches = append(batches, slices.Clone(docs[offset:end]))
		offset = end
	}
	return batches, nil
}
