package etl

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/tokenizer"
	"github.com/Tangerg/scope/etl/internal/tokenwindow"
)

const (
	defaultMaxTokensPerChunk = 800
	defaultMinTokensPerChunk = 350
	defaultMaxChunks         = 10_000
	// DefaultMaxSearchWork bounds token-window search per chunk.
	DefaultMaxSearchWork = 8 << 20
)

// ErrChunkLimitExceeded prevents token splitting from producing an unbounded
// number of documents.
var ErrChunkLimitExceeded = errors.New("etl: chunk limit exceeded")

// ErrChunkBudgetTooSmall means no nonempty trimmed source prefix fits the token budget.
var ErrChunkBudgetTooSmall = errors.New("etl: chunk token budget is too small")

// ErrSearchBudgetExceeded means prefix search stopped before proving whether
// a nonempty chunk fits. It is distinct from ErrChunkBudgetTooSmall.
var ErrSearchBudgetExceeded = errors.New("etl: token window search budget exceeded")

// TokenSplitterConfig configures token-aware chunking. Zero sizing values use
// documented defaults; negative values are rejected.
type TokenSplitterConfig struct {
	Tokenizer tokenizer.Tokenizer

	MaxTokensPerChunk int
	MinTokensPerChunk int
	MaxChunks         int
	// MaxSearchWork bounds bytes rendered or encoded and tokens decoded per
	// prefix search. Each operation costs at least one; zero uses DefaultMaxSearchWork.
	MaxSearchWork    int
	PreserveNewlines bool
	IDGenerator      IDGenerator
}

// TokenSplitter splits document text into token-bounded chunks and prefers a
// sentence boundary once the configured minimum token count has been reached.
type TokenSplitter struct {
	tokenizer         tokenizer.Tokenizer
	maxTokensPerChunk int
	minTokensPerChunk int
	maxChunks         int
	maxSearchWork     int
	preserveNewlines  bool
	splitter          *Splitter
}

// NewTokenSplitter validates token and chunk bounds before retaining the
// tokenizer.
func NewTokenSplitter(config TokenSplitterConfig) (*TokenSplitter, error) {
	if lo.IsNil(config.Tokenizer) {
		return nil, errors.New("etl: tokenizer is required")
	}
	if config.MaxTokensPerChunk < 0 || config.MinTokensPerChunk < 0 || config.MaxChunks < 0 || config.MaxSearchWork < 0 {
		return nil, errors.New("etl: token splitter limits must not be negative")
	}
	if config.MaxTokensPerChunk == 0 {
		config.MaxTokensPerChunk = defaultMaxTokensPerChunk
	}
	if config.MinTokensPerChunk == 0 {
		config.MinTokensPerChunk = min(defaultMinTokensPerChunk, config.MaxTokensPerChunk)
	}
	if config.MaxSearchWork == 0 {
		config.MaxSearchWork = DefaultMaxSearchWork
	}
	if config.MaxChunks == 0 {
		config.MaxChunks = defaultMaxChunks
	}
	if config.MinTokensPerChunk > config.MaxTokensPerChunk {
		return nil, fmt.Errorf(
			"etl: minimum chunk tokens %d exceed maximum %d",
			config.MinTokensPerChunk,
			config.MaxTokensPerChunk,
		)
	}

	splitter := &TokenSplitter{
		tokenizer:         config.Tokenizer,
		maxTokensPerChunk: config.MaxTokensPerChunk,
		minTokensPerChunk: config.MinTokensPerChunk,
		maxChunks:         config.MaxChunks,
		maxSearchWork:     config.MaxSearchWork,
		preserveNewlines:  config.PreserveNewlines,
	}
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

// SplitText emits trimmed chunks whose final text stays within the token budget.
// Each chunk uses an adaptively sized source probe and an exact final token
// measurement; chunk boundaries need not match a whole-document tokenization.
func (t *TokenSplitter) SplitText(ctx context.Context, text string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateTextEncoding(text); err != nil {
		return nil, err
	}
	text = t.clean(text)
	if text == "" {
		return nil, nil
	}

	var chunks []string
	for text != "" {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(chunks) == t.maxChunks {
			return nil, fmt.Errorf("%w: maximum is %d", ErrChunkLimitExceeded, t.maxChunks)
		}
		selected, err := t.nextChunk(ctx, text)
		if err != nil {
			return nil, err
		}
		text = strings.TrimSpace(text[len(selected):])
		chunks = append(chunks, strings.TrimSpace(selected))
	}
	return chunks, nil
}

func (t *TokenSplitter) nextChunk(ctx context.Context, source string) (string, error) {
	window, err := tokenwindow.Prefix(ctx, t.tokenizer, source, t.maxTokensPerChunk, t.maxSearchWork, strings.TrimSpace)
	if errors.Is(err, tokenwindow.ErrSearchBudgetExceeded) {
		return "", ErrSearchBudgetExceeded
	}
	if err != nil {
		return "", fmt.Errorf("etl: select token window: %w", err)
	}
	if window == "" {
		return "", fmt.Errorf("%w: maximum is %d", ErrChunkBudgetTooSmall, t.maxTokensPerChunk)
	}
	boundary := t.lastSentenceBoundary(window)
	if boundary <= 0 || boundary >= len(window) {
		return window, nil
	}
	prefix := window[:boundary]
	tokens, err := t.tokenizer.Encode(ctx, strings.TrimSpace(prefix))
	if err != nil {
		return "", fmt.Errorf("etl: measure sentence boundary: %w", err)
	}
	if len(tokens) < t.minTokensPerChunk || len(tokens) > t.maxTokensPerChunk {
		return window, nil
	}
	return prefix, nil
}

func (t *TokenSplitter) clean(text string) string {
	if !t.preserveNewlines {
		text = strings.ReplaceAll(text, "\n", " ")
	}
	return strings.TrimSpace(text)
}

func (*TokenSplitter) lastSentenceBoundary(text string) int {
	boundary := -1
	for _, punctuation := range []string{".", "?", "!", "\n", "。", "？", "！"} {
		if index := strings.LastIndex(text, punctuation); index >= 0 {
			boundary = max(boundary, index+len(punctuation))
		}
	}
	return boundary
}

// Split emits token-bounded document chunks with cloned metadata and lineage.
func (t *TokenSplitter) Split(ctx context.Context, docs []*document.Document) ([]*document.Document, error) {
	return t.splitter.Split(ctx, docs)
}
