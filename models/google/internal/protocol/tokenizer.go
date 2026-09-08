package protocol

import (
	"context"
	"errors"

	"google.golang.org/genai"

	"github.com/Tangerg/scope/core/tokenizer"
)

// TextEstimatorConfig configures a Gemini-backed token estimator.
// Token counts vary across model families — supply the same Model name
// you intend to send chat requests under so the count matches the real
// billing.
type TextEstimatorConfig struct {
	Client ClientConfig
	Model  string
}

func (t TextEstimatorConfig) Validate() error {
	if err := t.Client.Validate(); err != nil {
		return err
	}
	if t.Model == "" {
		return errors.New("google: Model is required")
	}
	return nil
}

var _ tokenizer.TextEstimator = (*TextEstimator)(nil)

// TextEstimator reports input-token counts via Gemini's count_tokens
// endpoint. Implements [tokenizer.TextEstimator] so it drops into code
// paths gating on token budgets (RAG chunking, prompt-window checks).
type TextEstimator struct {
	api   *api
	model string
}

// NewTextEstimator rejects an invalid provider/model binding before estimation begins.
func NewTextEstimator(ctx context.Context, config TextEstimatorConfig) (*TextEstimator, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	api, err := newAPI(ctx, config.Client)
	if err != nil {
		return nil, err
	}

	return &TextEstimator{api: api, model: config.Model}, nil
}

// EstimateText returns the prompt-token count Gemini would charge if
// text were sent as a single user message under the configured model.
func (t *TextEstimator) EstimateText(ctx context.Context, text string) (int, error) {
	contents := []*genai.Content{genai.NewContentFromText(text, genai.RoleUser)}
	resp, err := t.api.countTokens(ctx, t.model, contents, nil)
	if err != nil {
		return 0, err
	}
	return int(resp.TotalTokens), nil
}
