package anthropic

import (
	"context"
	"errors"
	"net/http"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"
)

type apiConfig struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
	Headers    http.Header
}

func (a apiConfig) validate() error {
	if a.APIKey == "" {
		return errors.New("anthropic: APIKey is required")
	}
	return nil
}

type api struct {
	client *anthropicsdk.Client
}

func newAPI(config apiConfig) (*api, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	options := []option.RequestOption{option.WithAPIKey(config.APIKey)}
	if config.BaseURL != "" {
		options = append(options, option.WithBaseURL(config.BaseURL))
	}
	if config.HTTPClient != nil {
		options = append(options, option.WithHTTPClient(config.HTTPClient))
	}
	for name, values := range config.Headers {
		for _, value := range values {
			options = append(options, option.WithHeader(name, value))
		}
	}
	client := anthropicsdk.NewClient(options...)

	return &api{client: &client}, nil
}

func (a *api) chatCompletion(ctx context.Context, req *anthropicsdk.MessageNewParams, opts ...option.RequestOption) (*anthropicsdk.Message, error) {
	if req == nil {
		return nil, errors.New("anthropic: request must not be nil")
	}
	return a.wrapResult(a.client.Messages.New(ctx, *req, opts...))
}

func (a *api) chatCompletionStream(ctx context.Context, req *anthropicsdk.MessageNewParams, opts ...option.RequestOption) *ssestream.Stream[anthropicsdk.MessageStreamEventUnion] {
	if req == nil {
		return nil
	}
	return a.client.Messages.NewStreaming(ctx, *req, opts...)
}

func (a *api) countTokens(ctx context.Context, req *anthropicsdk.MessageCountTokensParams, opts ...option.RequestOption) (*anthropicsdk.MessageTokensCount, error) {
	if req == nil {
		return nil, errors.New("anthropic: request must not be nil")
	}
	return a.wrapResult(a.client.Messages.CountTokens(ctx, *req, opts...))
}

func (*api) wrapError(err error) error {
	if err == nil {
		return nil
	}
	type httpError interface {
		error
		HTTPStatus() int
		HTTPHeader() http.Header
	}
	if _, ok := errors.AsType[httpError](err); ok {
		return err
	}
	apiErr, ok := errors.AsType[*anthropicsdk.Error](err)
	if !ok {
		return err
	}
	var header http.Header
	if apiErr.Response != nil {
		header = apiErr.Response.Header.Clone()
	}
	return &responseError{err: err, status: apiErr.StatusCode, header: header}
}

func (a *api) wrapResult[T any](value *T, err error) (*T, error) {
	return value, a.wrapError(err)
}
