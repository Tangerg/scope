package luma

import (
	"cmp"
	"context"
	"errors"
	"net/http"

	lumaagents "github.com/lumalabs/luma-agents-go"
	lumaoption "github.com/lumalabs/luma-agents-go/option"
)

type apiConfig struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

func (a apiConfig) validate() error {
	if a.APIKey == "" {
		return errors.New("luma: APIKey is required")
	}
	return nil
}

type api struct {
	client *lumaagents.Client
}

func newAPI(config apiConfig) (*api, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	requestOptions := []lumaoption.RequestOption{
		lumaoption.WithAuthToken(config.APIKey),
		lumaoption.WithBaseURL(cmp.Or(config.BaseURL, DefaultBaseURL)),
	}
	if config.HTTPClient != nil {
		requestOptions = append(requestOptions, lumaoption.WithHTTPClient(config.HTTPClient))
	}
	return &api{client: lumaagents.NewClient(requestOptions...)}, nil
}

func (a *api) createGeneration(ctx context.Context, params lumaagents.GenerationNewParams) (*lumaagents.Generation, error) {
	if a == nil || a.client == nil || a.client.Generations == nil {
		return nil, errors.New("luma: nil API")
	}
	return admitGeneration(a.client.Generations.New(ctx, params))
}

func (a *api) getGeneration(ctx context.Context, generationID string) (*lumaagents.Generation, error) {
	if a == nil || a.client == nil || a.client.Generations == nil {
		return nil, errors.New("luma: nil API")
	}
	if generationID == "" {
		return nil, errors.New("luma: generation ID is required")
	}
	return admitGeneration(a.client.Generations.Get(ctx, generationID))
}

func admitGeneration(generation *lumaagents.Generation, err error) (*lumaagents.Generation, error) {
	if err != nil {
		return nil, err
	}
	if generation == nil {
		return nil, errors.New("luma: generation response must be an object")
	}
	return generation, nil
}
