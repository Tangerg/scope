package openai

import (
	"context"
	"errors"
	"net/http"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/responses"
)

type apiConfig struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
	Headers    http.Header
}

func (a apiConfig) validate() error {
	if a.APIKey == "" {
		return errors.New("openai: APIKey is required")
	}
	return nil
}

type api struct {
	client *openai.Client
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
	client := openai.NewClient(options...)

	return &api{client: &client}, nil
}

func (a *api) chatCompletion(ctx context.Context, req *openai.ChatCompletionNewParams, opts ...option.RequestOption) (*openai.ChatCompletion, error) {
	if req == nil {
		return nil, errors.New("openai: request must not be nil")
	}
	return a.wrapResult(a.client.Chat.Completions.New(ctx, *req, opts...))
}

func (a *api) chatCompletionStream(ctx context.Context, req *openai.ChatCompletionNewParams, opts ...option.RequestOption) (*ssestream.Stream[openai.ChatCompletionChunk], error) {
	if req == nil {
		return nil, errors.New("openai: request must not be nil")
	}
	return a.client.Chat.Completions.NewStreaming(ctx, *req, opts...), nil
}

func (a *api) responseNew(ctx context.Context, req *responses.ResponseNewParams, opts ...option.RequestOption) (*responses.Response, error) {
	if req == nil {
		return nil, errors.New("openai: request must not be nil")
	}
	return a.wrapResult(a.client.Responses.New(ctx, *req, opts...))
}

func (a *api) responseNewStream(ctx context.Context, req *responses.ResponseNewParams, opts ...option.RequestOption) (*ssestream.Stream[responses.ResponseStreamEventUnion], error) {
	if req == nil {
		return nil, errors.New("openai: request must not be nil")
	}
	return a.client.Responses.NewStreaming(ctx, *req, opts...), nil
}

func (a *api) responseInputTokensCount(ctx context.Context, req *responses.InputTokenCountParams, opts ...option.RequestOption) (*responses.InputTokenCountResponse, error) {
	if req == nil {
		return nil, errors.New("openai: input token count request must not be nil")
	}
	return a.wrapResult(a.client.Responses.InputTokens.Count(ctx, *req, opts...))
}

func (a *api) embedding(ctx context.Context, req *openai.EmbeddingNewParams, opts ...option.RequestOption) (*openai.CreateEmbeddingResponse, error) {
	if req == nil {
		return nil, errors.New("openai: request must not be nil")
	}
	return a.wrapResult(a.client.Embeddings.New(ctx, *req, opts...))
}

func (a *api) image(ctx context.Context, req *openai.ImageGenerateParams, opts ...option.RequestOption) (*openai.ImagesResponse, error) {
	if req == nil {
		return nil, errors.New("openai: request must not be nil")
	}
	return a.wrapResult(a.client.Images.Generate(ctx, *req, opts...))
}

func (a *api) moderation(ctx context.Context, req *openai.ModerationNewParams, opts ...option.RequestOption) (*openai.ModerationNewResponse, error) {
	if req == nil {
		return nil, errors.New("openai: request must not be nil")
	}
	return a.wrapResult(a.client.Moderations.New(ctx, *req, opts...))
}

func (a *api) audioTTS(ctx context.Context, req *openai.AudioSpeechNewParams, opts ...option.RequestOption) (*http.Response, error) {
	if req == nil {
		return nil, errors.New("openai: request must not be nil")
	}
	return a.wrapResult(a.client.Audio.Speech.New(ctx, *req, opts...))
}

func (a *api) audioTranscription(ctx context.Context, req *openai.AudioTranscriptionNewParams, opts ...option.RequestOption) (*openai.AudioTranscriptionNewResponseUnion, error) {
	if req == nil {
		return nil, errors.New("openai: request must not be nil")
	}
	return a.wrapResult(a.client.Audio.Transcriptions.New(ctx, *req, opts...))
}

func (a *api) audioTranslation(ctx context.Context, req *openai.AudioTranslationNewParams, opts ...option.RequestOption) (*openai.Translation, error) {
	if req == nil {
		return nil, errors.New("openai: request must not be nil")
	}
	return a.wrapResult(a.client.Audio.Translations.New(ctx, *req, opts...))
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
	apiErr, ok := errors.AsType[*openai.Error](err)
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
