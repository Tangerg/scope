package tokenizer

import (
	"context"
	"errors"
	"fmt"
)

// BatchConfig bounds an ordered group of rendered texts by item count, tokens
// or both. The zero value keeps all texts in one group.
type BatchConfig struct {
	MaxItems int
	Tokens   Limit
}

func (b BatchConfig) Validate() error {
	if b.MaxItems < 0 {
		return errors.New("tokenizer: batch item limit must not be negative")
	}
	return nil
}

// Batches measures the exact supplied texts once and returns ordered slices
// backed by that input. Every item fits its limit. Errors return no slices.
func (b BatchConfig) Batches(ctx context.Context, texts []string) ([][]string, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var groups [][]string
	start, tokens := 0, 0
	for i, text := range texts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, err := b.Tokens.Count(ctx, text)
		if err != nil {
			return nil, fmt.Errorf("tokenizer: count text %d: %w", i, err)
		}
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if b.Tokens.Bounded() && n > b.Tokens.Max() {
			return nil, fmt.Errorf("tokenizer: text %d costs %d tokens, budget %d", i, n, b.Tokens.Max())
		}
		if i > start && (b.MaxItems > 0 && i-start >= b.MaxItems || b.Tokens.Bounded() && n > b.Tokens.Max()-tokens) {
			groups = append(groups, texts[start:i:i])
			start = i
			tokens = 0
		}
		tokens += n
	}
	if start < len(texts) {
		groups = append(groups, texts[start:len(texts):len(texts)])
	}
	return groups, nil
}
