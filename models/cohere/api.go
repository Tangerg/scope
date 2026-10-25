package cohere

import (
	"cmp"
	"context"
	"errors"
	"net/http"

	"github.com/Tangerg/scope/core/httpclient"

	cohere "github.com/cohere-ai/cohere-go/v2"
	coherecore "github.com/cohere-ai/cohere-go/v2/core"
	"github.com/cohere-ai/cohere-go/v2/option"
	cohereclientv2 "github.com/cohere-ai/cohere-go/v2/v2"
)

const defaultBaseURL = "https://api.cohere.com"

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

func newAPI(ctx context.Context, config apiConfig) (*api, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}

	baseURL := cmp.Or(config.BaseURL, defaultBaseURL)
	source := config.HTTPClient
	if source == nil {
		source = http.DefaultClient
	}
	client, err := httpclient.New(ctx, httpclient.Config{HTTPClient: source, BaseURL: baseURL})
	if err != nil {
		return nil, err
	}
	options := []coherecore.RequestOption{option.WithToken(config.APIKey), option.WithBaseURL(baseURL), option.WithHTTPClient(&apiHTTPClient{client: client})}
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
