package anthropic

import (
	"context"
	"errors"
	"net/http"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"

	"github.com/Tangerg/scope/core/tokenizer"
)

// TextCounterConfig configures an Anthropic-backed token estimator.
// Model picks the tokenizer vocabulary Anthropic counts against; a
// mismatch (Claude 3 model name vs Claude 4 vocab) produces wrong
// counts.
type TextCounterConfig struct {
	APIKey     string
	Model      string
	BaseURL    string
	HTTPClient *http.Client
}

func (t TextCounterConfig) Validate() error {
	if t.APIKey == "" {
		return errors.New("anthropic: APIKey is required")
	}
	if t.Model == "" {
		return errors.New("anthropic: Model is required")
	}
	return nil
}

var _ tokenizer.TextCounter = (*TextCounter)(nil)

// TextCounter reports input-token counts via Anthropic's
// /messages/count_tokens endpoint. Implements [tokenizer.TextCounter]
// so it drops into code paths already gating on token budgets (RAG
// chunking, prompt-window checks, cost preflight).
//
// Every estimate is a network round-trip; for high-QPS counting reach
// for an offline tokenizer instead.
type TextCounter struct {
	api   *api
	model string
}

// NewTextCounter rejects an invalid provider/model binding before estimation begins.
func NewTextCounter(_ context.Context, config TextCounterConfig) (*TextCounter, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	api, err := newAPI(apiConfig{
		APIKey:     config.APIKey,
		BaseURL:    config.BaseURL,
		HTTPClient: config.HTTPClient,
	})
	if err != nil {
		return nil, err
	}

	return &TextCounter{api: api, model: config.Model}, nil
}

// CountText returns the prompt-token count Anthropic would charge if
// text were sent as a single user message under the configured model.
func (t *TextCounter) CountText(ctx context.Context, text string) (int, error) {
	resp, err := t.api.countTokens(ctx, &anthropicsdk.MessageCountTokensParams{
		Model: anthropicsdk.Model(t.model),
		Messages: []anthropicsdk.MessageParam{
			anthropicsdk.NewUserMessage(anthropicsdk.NewTextBlock(text)),
		},
	})
	if err != nil {
		return 0, err
	}
	return int(resp.InputTokens), nil
}
