package vertexai

import (
	"context"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/models/google/internal/protocol"
)

// EmbeddingModelConfig binds provider access and defaults shared by every embedding call.
type EmbeddingModelConfig struct {
	Client         ClientConfig
	DefaultOptions embedding.Options
}

func (e EmbeddingModelConfig) Validate() error {
	return e.protocol().Validate()
}

func (e EmbeddingModelConfig) protocol() protocol.EmbeddingModelConfig {
	return protocol.EmbeddingModelConfig{
		Provider:       protocolProvider,
		Client:         e.Client.protocol(),
		DefaultOptions: e.DefaultOptions,
	}
}

var _ embedding.Model = (*EmbeddingModel)(nil)

// EmbeddingModel is the shared protocol type itself rather than a wrapper,
// so this provider adds no second public surface for callers to choose
// between.
type EmbeddingModel = callModel[embedding.Request, embedding.Response]

// NewEmbeddingModel rejects an invalid provider binding before the first embedding call.
func NewEmbeddingModel(ctx context.Context, config EmbeddingModelConfig) (*EmbeddingModel, error) {
	return newCallModel[embedding.Request, embedding.Response](protocol.NewEmbeddingModel(ctx, config.protocol()))
}
