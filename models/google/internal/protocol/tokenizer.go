package protocol

import (
	"context"
	"errors"

	"google.golang.org/genai"

	"github.com/Tangerg/scope/core/tokenizer"
)

// TextCounterConfig configures a Gemini-backed token counter.
// Token counts vary across model families — supply the same Model name
// you intend to send chat requests under so the count matches the real
// billing.
type TextCounterConfig struct {
	Client ClientConfig
	Model  string
}

func (t TextCounterConfig) Validate() error {
	if err := t.Client.Validate(); err != nil {
		return err
	}
	if t.Model == "" {
		return errors.New("google: Model is required")
	}
	return nil
}

var _ tokenizer.TextCounter = (*TextCounter)(nil)

// TextCounter reports input-token counts via Gemini's count_tokens
// endpoint. Implements [tokenizer.TextCounter] so it drops into code
// paths gating on token budgets (RAG chunking, prompt-window checks).
type TextCounter struct {
	api   *api
	model string
}

// NewTextCounter rejects an invalid provider/model binding before counting begins.
func NewTextCounter(ctx context.Context, config TextCounterConfig) (*TextCounter, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	api, err := newAPI(ctx, config.Client)
	if err != nil {
		return nil, err
	}

	return &TextCounter{api: api, model: config.Model}, nil
}

// CountText returns the prompt-token count Gemini would charge if
// text were sent as a single user message under the configured model.
func (t *TextCounter) CountText(ctx context.Context, text string) (int, error) {
	contents := []*genai.Content{genai.NewContentFromText(text, genai.RoleUser)}
	resp, err := t.api.countTokens(ctx, t.model, contents, nil)
	if err != nil {
		return 0, err
	}
	return int(resp.TotalTokens), nil
}
