package mistral

import (
	"cmp"
	"context"
	"errors"
	"net/http"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/models/protocol/openai"
)

// EmbeddingModelConfig binds provider access and defaults shared by every embedding call.
type EmbeddingModelConfig struct {
	APIKey         string
	DefaultOptions embedding.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (e EmbeddingModelConfig) Validate() error {
	if e.APIKey == "" {
		return errors.New("mistral: API key is required")
	}
	if e.DefaultOptions.Model == "" {
		return errors.New("mistral: default model is required")
	}
	if err := e.DefaultOptions.Validate(); err != nil {
		return err
	}
	return nil
}

var _ embedding.Model = (*EmbeddingModel)(nil)

// EmbeddingModel is the shared protocol type itself rather than a wrapper,
// so this provider adds no second public surface for callers to choose
// between.
type EmbeddingModel = openai.EmbeddingModel

// NewEmbeddingModel rejects an invalid provider binding before the first embedding call.
func NewEmbeddingModel(ctx context.Context, config EmbeddingModelConfig) (*EmbeddingModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return openai.NewEmbeddingModel(ctx, openai.EmbeddingModelConfig{
		Provider:       "mistral",
		APIKey:         config.APIKey,
		DefaultOptions: config.DefaultOptions,
		BaseURL:        cmp.Or(config.BaseURL, DefaultBaseURL),
		HTTPClient:     config.HTTPClient,
	})
}
