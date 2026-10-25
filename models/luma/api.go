package luma

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"

	lumaagents "github.com/lumalabs/luma-agents-go"
	lumaoption "github.com/lumalabs/luma-agents-go/option"

	"github.com/Tangerg/scope/core/httpclient"
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

// The SDK's default client factory is private; an origin-bound client must
// preserve its header wait limit without imposing a deadline on response bodies.
func (a apiConfig) httpClient() *http.Client {
	if a.HTTPClient != nil {
		return a.HTTPClient
	}
	transport := http.DefaultTransport
	if standard, ok := transport.(*http.Transport); ok && standard != nil {
		owned := standard.Clone()
		owned.ResponseHeaderTimeout = defaultResponseHeaderTimeout
		transport = owned
	}
	return &http.Client{Transport: transport}
}

type api struct {
	client *lumaagents.Client
}

func newAPI(ctx context.Context, config apiConfig) (*api, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	baseURL := cmp.Or(config.BaseURL, DefaultBaseURL)
	boundClient, err := httpclient.New(ctx, httpclient.Config{HTTPClient: config.httpClient(), BaseURL: baseURL})
	if err != nil {
		return nil, fmt.Errorf("luma: HTTP client: %w", err)
	}
	requestOptions := []lumaoption.RequestOption{
		lumaoption.WithAuthToken(config.APIKey),
		lumaoption.WithBaseURL(baseURL),
		lumaoption.WithHTTPClient(boundClient),
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
