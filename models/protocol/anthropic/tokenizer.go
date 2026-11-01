package anthropic

import (
	"context"
	"errors"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go/v2"

	"github.com/Tangerg/scope/core/tokenizer"
)

var _ tokenizer.TextCounter = (*TextCounter)(nil)

// TextCounter reports input-token counts via Anthropic's
// /messages/count_tokens endpoint. Implements [tokenizer.TextCounter]
// so it drops into code paths already gating on token budgets (RAG
// chunking, prompt-window checks, cost preflight).
//
// Every count is a network round-trip; for high-QPS counting reach
// for an offline tokenizer instead.
type TextCounter struct {
	api   *api
	model string
}

// CountText returns the prompt-token count Anthropic would charge if
// text were sent as a single user message under the configured model.
func (t *TextCounter) CountText(ctx context.Context, text string) (int, error) {
	tokens, err := t.api.countTokens(ctx, &anthropicsdk.MessageCountTokensParams{
		Model: anthropicsdk.Model(t.model),
		Messages: []anthropicsdk.MessageParam{
			anthropicsdk.NewUserMessage(anthropicsdk.NewTextBlock(text)),
		},
	})
	if err != nil {
		return 0, err
	}
	count := int(tokens)
	if int64(count) != tokens {
		return 0, errors.New("anthropic: input_tokens count exceeds int")
	}
	return count, nil
}
