package tokenizer

import (
	"context"
	"errors"
	"fmt"

	"github.com/samber/lo"
)

// BatchConfig bounds an ordered group of rendered texts by item count, token
// count or both. The zero value keeps all texts in one group. Counter is
// required exactly when MaxTokens is positive.
type BatchConfig struct {
	MaxItems  int
	MaxTokens int
	Counter   TextCounter
}

func (b BatchConfig) Validate() error {
	if b.MaxItems < 0 || b.MaxTokens < 0 || b.Counter != nil && lo.IsNil(b.Counter) || (b.Counter != nil) != (b.MaxTokens > 0) {
		return errors.New("tokenizer: nonnegative batch limits and a counter for token limits are required")
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
		n := 0
		if b.Counter != nil {
			var err error
			n, err = b.Counter.CountText(ctx, text)
			if err != nil {
				return nil, fmt.Errorf("tokenizer: count text %d: %w", i, err)
			}
			if err = ctx.Err(); err != nil {
				return nil, err
			}
			if n < 0 || n > b.MaxTokens {
				return nil, fmt.Errorf("tokenizer: text %d costs %d tokens, budget %d", i, n, b.MaxTokens)
			}
		}
		if i > start && (b.MaxItems > 0 && i-start >= b.MaxItems || b.MaxTokens > 0 && n > b.MaxTokens-tokens) {
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
