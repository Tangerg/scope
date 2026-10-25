package cohere

import (
	"context"
	"errors"
	"net/http"

	cohere "github.com/cohere-ai/cohere-go/v2"
	coherecore "github.com/cohere-ai/cohere-go/v2/core"
	"github.com/cohere-ai/cohere-go/v2/option"
	cohereclientv2 "github.com/cohere-ai/cohere-go/v2/v2"
)

type apiConfig struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

func (a apiConfig) validate() error {
	if a.APIKey == "" {
		return errors.New("cohere: APIKey is required")
	}
	return nil
}

type api struct {
	v2 *cohereclientv2.Client
}

func newAPI(config apiConfig) (*api, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}

	options := []coherecore.RequestOption{option.WithToken(config.APIKey)}
	if config.BaseURL != "" {
		options = append(options, option.WithBaseURL(config.BaseURL))
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	options = append(options, option.WithHTTPClient(&apiHTTPClient{client: httpClient}))
	reqOpts := coherecore.NewRequestOptions(options...)

	return &api{v2: cohereclientv2.NewClient(reqOpts)}, nil
}

func (a *api) rerank(ctx context.Context, req *cohere.V2RerankRequest) (*cohere.V2RerankResponse, error) {
	if req == nil {
		return nil, errors.New("cohere: request must not be nil")
	}
	return a.v2.Rerank(ctx, req)
}

func (a *api) embed(ctx context.Context, req *cohere.V2EmbedRequest) (*cohere.EmbedByTypeResponse, error) {
	if req == nil {
		return nil, errors.New("cohere: request must not be nil")
	}
	return a.v2.Embed(ctx, req)
}
