package embeddingclient

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/embedding"
)

var ErrNilModel = errors.New("embeddingclient: nil model")

type Config struct {
	Model   embedding.Model
	Options embedding.Options
}

// Client validates both model boundaries, preserves input order, and returns
// independently owned vectors. It is immutable; concurrent use requires a
// concurrency-safe Model. Provider metadata remains available through the model.
type Client struct {
	model   embedding.Model
	options embedding.Options
}

func New(config Config) (Client, error) {
	if lo.IsNil(config.Model) {
		return Client{}, ErrNilModel
	}
	if err := config.Options.Validate(); err != nil {
		return Client{}, err
	}
	return Client{model: config.Model, options: config.Options.Clone()}, nil
}

func (c Client) EmbedTexts(ctx context.Context, texts []string) ([][]float64, error) {
	if lo.IsNil(c.model) {
		return nil, ErrNilModel
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	request, err := embedding.NewRequest(texts)
	if err != nil {
		return nil, fmt.Errorf("embeddingclient: embed texts: %w", err)
	}
	request.Options = c.options
	response, err := c.model.Call(ctx, request.Clone())
	if err != nil {
		return nil, fmt.Errorf("embeddingclient: embed texts: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := response.ValidateFor(request); err != nil {
		return nil, fmt.Errorf("embeddingclient: embed texts: invalid model response: %w", err)
	}

	vectors := make([][]float64, len(response.Outputs))
	for i, output := range response.Outputs {
		vectors[i] = slices.Clone(output.Embedding)
	}
	return vectors, nil
}

func (c Client) EmbedText(ctx context.Context, text string) ([]float64, error) {
	vectors, err := c.EmbedTexts(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	return vectors[0], nil
}
