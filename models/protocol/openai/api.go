package openai

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"

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
	_, err := a.requestOptions()
	return err
}

func (a apiConfig) requestOptions() ([]option.RequestOption, error) {
	if a.APIKey == "" {
		return nil, errors.New("openai: APIKey is required")
	}
	environmentURL, environmentPresent := os.LookupEnv("OPENAI_BASE_URL")
	for _, source := range []struct {
		name    string
		value   string
		present bool
	}{
		{"BaseURL", a.BaseURL, a.BaseURL != ""},
		{"OPENAI_BASE_URL", environmentURL, environmentPresent},
	} {
		if !source.present {
			continue
		}
		endpoint, err := url.Parse(source.value)
		if err != nil {
			return nil, fmt.Errorf("openai: %s: %w", source.name, err)
		}
		if (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Hostname() == "" {
			return nil, fmt.Errorf("openai: %s must be an absolute HTTP or HTTPS URL with a host", source.name)
		}
	}
	options := []option.RequestOption{option.WithAPIKey(a.APIKey)}
	baseURL := a.BaseURL
	if baseURL == "" && environmentPresent {
		baseURL = environmentURL
	}
	if baseURL != "" {
		options = append(options, option.WithBaseURL(baseURL))
	}
	if a.HTTPClient != nil {
		options = append(options, option.WithHTTPClient(a.HTTPClient))
	}
	for name, values := range a.Headers {
		for _, value := range values {
			options = append(options, option.WithHeader(name, value))
		}
	}
	return options, nil
}

type api struct {
	client *openai.Client
}

func newAPI(config apiConfig) (*api, error) {
	options, err := config.requestOptions()
	if err != nil {
		return nil, err
	}
	client := openai.NewClient(options...)
	return &api{client: &client}, nil
}

func (a *api) listModels(ctx context.Context, maxModels int, maxResponseBytes int64) ([]string, error) {
	if maxModels <= 0 || maxResponseBytes <= 0 {
		return nil, errors.New("openai: model count and response byte limits must be positive")
	}
	budget := modelListResponseBudget{remaining: maxResponseBytes}
	page, err := a.client.Models.List(ctx, option.WithMaxRetries(0), option.WithMiddleware(budget.read))
	if err != nil {
		return nil, a.wrapError(err)
	}
	if page == nil || !page.JSON.Data.Valid() {
		return nil, errors.New("openai: model list response must contain a data array")
	}
	if field, exists := page.JSON.ExtraFields["has_more"]; exists && field.Raw() != "false" {
		return nil, errors.New("openai: model list response declares unsupported pagination")
	}
	if len(page.Data) > maxModels {
		return nil, fmt.Errorf("openai: model list exceeds %d-model limit", maxModels)
	}
	ids := make([]string, 0, len(page.Data))
	for _, model := range page.Data {
		if !model.JSON.ID.Valid() || jsontext.Value(model.JSON.ID.Raw()).Kind() != '"' || model.ID == "" {
			return nil, errors.New("openai: model list response contains an invalid model ID")
		}
		ids = append(ids, model.ID)
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	return ids, nil
}

func (a *api) chatCompletionStream(ctx context.Context, req *openai.ChatCompletionNewParams, opts ...option.RequestOption) (*ssestream.Stream[openai.ChatCompletionChunk], error) {
	if req == nil {
		return nil, errors.New("openai: request must not be nil")
	}
	return a.client.Chat.Completions.NewStreaming(ctx, *req, opts...), nil
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

func (a *api) speech(ctx context.Context, req *openai.AudioSpeechNewParams, opts ...option.RequestOption) (*http.Response, error) {
	if req == nil {
		return nil, errors.New("openai: request must not be nil")
	}
	return a.wrapResult(a.client.Audio.Speech.New(ctx, *req, opts...))
}

func (a *api) transcription(ctx context.Context, req *openai.AudioTranscriptionNewParams, opts ...option.RequestOption) (*openai.AudioTranscriptionNewResponseUnion, error) {
	if req == nil {
		return nil, errors.New("openai: request must not be nil")
	}
	return a.wrapResult(a.client.Audio.Transcriptions.New(ctx, *req, opts...))
}

func (a *api) translation(ctx context.Context, req *openai.AudioTranslationNewParams, opts ...option.RequestOption) (*openai.Translation, error) {
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
