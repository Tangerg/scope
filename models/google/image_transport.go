package google

import (
	"cmp"
	"context"
	jsonv2 "encoding/json/v2"
	"fmt"
	"slices"

	"github.com/go-resty/resty/v2"

	"github.com/Tangerg/scope/core/httpclient"
)

const (
	// Inline high-resolution images have a larger wire budget than ordinary JSON.
	maxImageInteractionResponseBytes = 256 << 20
	imageInteractionsPath            = "/v1beta/interactions"
	imageAPIKeyHeader                = "x-goog-api-key"
	imageJSONMediaType               = "application/json"
)

type imageTransport struct{ client *resty.Client }

func newImageTransport(ctx context.Context, config ImageModelConfig) (*imageTransport, error) {
	endpoint := cmp.Or(config.BaseURL, DefaultBaseURL)
	borrowed, err := httpclient.New(ctx, httpclient.Config{HTTPClient: config.HTTPClient, BaseURL: endpoint})
	if err != nil {
		return nil, err
	}
	client := resty.NewWithClient(borrowed).SetResponseBodyLimit(maxImageInteractionResponseBytes)
	client.SetJSONMarshaler(func(value any) ([]byte, error) { return jsonv2.Marshal(value) }).
		SetJSONUnmarshaler(func(data []byte, value any) error { return jsonv2.Unmarshal(data, value) }).
		SetBaseURL(endpoint).
		SetHeader(imageAPIKeyHeader, config.APIKey).
		SetHeader("Content-Type", imageJSONMediaType)
	return &imageTransport{client: client}, nil
}

func (i *imageTransport) create(ctx context.Context, req *imageInteractionRequest) (*imageInteractionResponse, error) {

	var out imageInteractionResponse
	resp, err := i.client.R().
		SetContext(ctx).
		SetBody(req).
		SetResult(&out).
		Post(imageInteractionsPath)
	if err != nil {
		return nil, fmt.Errorf("google: image: create interaction: %w", err)
	}
	if !resp.IsSuccess() {
		return nil, fmt.Errorf("google: image: %w", httpclient.NewResponseError(resp.StatusCode(), resp.Header()))
	}
	out.Raw = slices.Clone(resp.Body())
	return &out, nil
}
