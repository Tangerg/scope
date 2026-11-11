package openai

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/Tangerg/scope/core/httpclient"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/responses"
)

const (
	defaultAPIBaseURL            = "https://api.openai.com/v1/"
	defaultResponseHeaderTimeout = 10 * time.Minute
)

const (
	headerAuthorization      = "Authorization"
	headerProxyAuthorization = "Proxy-Authorization"
	headerAPIKey             = "Api-Key"
	headerXAPIKey            = "X-Api-Key"
	headerCookie             = "Cookie"
)

type apiConfig struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
	Headers    http.Header
}

// A compatible provider must never send its credential to the official
// endpoint or an unrelated SDK environment default.
func requireBindingEndpoint(provider, endpoint string) error {
	if provider != ProviderID && endpoint == "" {
		return errors.New("openai protocol: a compatible binding requires an explicit endpoint")
	}
	return nil
}

// validateBinding admits the provider, credential, and endpoint every
// OpenAI-protocol model binds before it is constructed.
func validateBinding(provider, apiKey, baseURL string) error {
	if err := requireBindingEndpoint(provider, baseURL); err != nil {
		return err
	}
	if err := validateProvider(provider); err != nil {
		return fmt.Errorf("openai: Provider: %w", err)
	}
	return apiConfig{APIKey: apiKey, BaseURL: baseURL}.validate()
}

func (a apiConfig) validate() error {
	_, err := a.endpoint()
	return err
}

func (a apiConfig) endpoint() (string, error) {
	if a.APIKey == "" {
		return "", errors.New("openai: APIKey is required")
	}
	baseURL, source, present := a.BaseURL, "BaseURL", a.BaseURL != ""
	if !present {
		baseURL, present = os.LookupEnv("OPENAI_BASE_URL")
		source = "OPENAI_BASE_URL"
	}
	if !present {
		baseURL = defaultAPIBaseURL
	}
	endpoint, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("openai: %s: %w", source, err)
	}
	if _, err := httpclient.NewOrigin(endpoint); err != nil {
		return "", fmt.Errorf("openai: %s: %w", source, err)
	}

	return baseURL, nil
}

func (a apiConfig) requestOptions(ctx context.Context) ([]option.RequestOption, error) {
	baseURL, err := a.endpoint()
	if err != nil {
		return nil, err
	}
	client, err := httpclient.New(ctx, httpclient.Config{HTTPClient: a.httpClient(), BaseURL: baseURL})
	if err != nil {
		return nil, err
	}
	options := []option.RequestOption{option.WithAPIKey(a.APIKey)}
	options = append(options, option.WithBaseURL(baseURL), option.WithHTTPClient(client))
	headers := make(http.Header, len(a.Headers))
	for name, values := range a.Headers {
		key := http.CanonicalHeaderKey(name)
		switch key {
		case headerAuthorization, headerProxyAuthorization, headerAPIKey, headerXAPIKey, headerCookie:
			return nil, fmt.Errorf("openai: header %q is owned by the credential binding", key)
		}
		if _, duplicate := headers[key]; duplicate {
			return nil, fmt.Errorf("openai: duplicate header %q", key)
		}
		headers[key] = values
	}
	for name, values := range headers {
		for _, value := range values {
			options = append(options, option.WithHeader(name, value))
		}
	}
	return options, nil
}

// Direct service construction skips SDK defaults along with ambient settings.
// Its header deadline must not become a deadline on long-lived response bodies.
func (a apiConfig) httpClient() *http.Client {
	if a.HTTPClient != nil {
		return a.HTTPClient
	}
	transport := http.DefaultTransport
	if standard, ok := transport.(*http.Transport); ok {
		owned := standard.Clone()
		owned.ResponseHeaderTimeout = defaultResponseHeaderTimeout
		transport = owned
	}
	return &http.Client{Transport: transport}
}

type api struct {
	chat        openai.ChatCompletionService
	responses   responses.ResponseService
	embeddings  openai.EmbeddingService
	images      openai.ImageService
	moderations openai.ModerationService
	audio       openai.AudioService
	models      openai.ModelService
}

func newAPI(ctx context.Context, config apiConfig) (*api, error) {
	options, err := config.requestOptions(ctx)
	if err != nil {
		return nil, err
	}
	// Services apply explicit options without reloading SDK environment defaults.
	// The selected binding must not inherit another provider's ambient settings.
	return &api{
		chat: openai.NewChatCompletionService(options...), responses: responses.NewResponseService(options...),
		embeddings: openai.NewEmbeddingService(options...), images: openai.NewImageService(options...),
		moderations: openai.NewModerationService(options...), audio: openai.NewAudioService(options...),
		models: openai.NewModelService(options...),
	}, nil
}

func (a *api) listModels(ctx context.Context, maxModels int, maxResponseBytes int64) ([]string, error) {
	if maxModels <= 0 || maxResponseBytes <= 0 {
		return nil, errors.New("openai: model count and response byte limits must be positive")
	}
	budget, err := httpclient.NewResponseBudget(maxResponseBytes)
	if err != nil {
		return nil, err
	}
	page, err := a.models.List(ctx, option.WithMaxRetries(0), option.WithMiddleware(budget.Apply))
	if err != nil {
		return nil, a.wrapError(err)
	}
	if page == nil || !page.JSON.Data.Valid() {
		return nil, errors.New("openai: model list response must contain a data array")
	}
	if field, exists := page.JSON.ExtraFields["has_more"]; exists && !falseJSON([]byte(field.Raw())) {
		return nil, errors.New("openai: model list response declares unsupported pagination")
	}
	if len(page.Data) > maxModels {
		return nil, fmt.Errorf("openai: model list exceeds %d-model limit", maxModels)
	}
	ids := make([]string, 0, len(page.Data))
	for _, model := range page.Data {
		if !model.JSON.ID.Valid() || jsontext.Value(model.JSON.ID.Raw()).Kind() != jsontext.KindString || model.ID == "" {
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
	opts = append(opts, option.WithMiddleware(httpclient.ResponseBodyLimit(httpclient.DefaultMaxJSONResponseBytes).ApplyEventStream))
	return a.chat.NewStreaming(ctx, *req, opts...), nil
}

func (a *api) responseNewStream(ctx context.Context, req *responses.ResponseNewParams, opts ...option.RequestOption) (*ssestream.Stream[responses.ResponseStreamEventUnion], error) {
	if req == nil {
		return nil, errors.New("openai: request must not be nil")
	}
	opts = append(opts, option.WithMiddleware(httpclient.ResponseBodyLimit(httpclient.DefaultMaxJSONResponseBytes).ApplyEventStream))
	return a.responses.NewStreaming(ctx, *req, opts...), nil
}

func (a *api) responseInputTokensCount(ctx context.Context, req *responses.InputTokenCountParams, opts ...option.RequestOption) (int64, error) {
	if req == nil {
		return 0, errors.New("openai: input token count request must not be nil")
	}
	opts = append(opts, option.WithMiddleware(httpclient.ResponseBodyLimit(httpclient.DefaultMaxJSONResponseBytes).Apply))
	response, err := a.wrapResult(a.responses.InputTokens.Count(ctx, *req, opts...))
	if err != nil {
		return 0, err
	}
	if response == nil {
		return 0, errors.New("openai: missing input_tokens count")
	}
	count, err := strconv.ParseInt(response.JSON.InputTokens.Raw(), 10, 64)
	if err != nil || count < 0 {
		return 0, errors.New("openai: invalid input_tokens count")
	}
	return count, nil
}

func (a *api) embedding(ctx context.Context, req *openai.EmbeddingNewParams, opts ...option.RequestOption) (*openai.CreateEmbeddingResponse, error) {
	if req == nil {
		return nil, errors.New("openai: request must not be nil")
	}
	opts = append(opts, option.WithMiddleware(httpclient.ResponseBodyLimit(httpclient.DefaultMaxJSONResponseBytes).Apply))
	return a.wrapResult(a.embeddings.New(ctx, *req, opts...))
}

func (a *api) image(ctx context.Context, req *openai.ImageGenerateParams, opts ...option.RequestOption) (*openai.ImagesResponse, error) {
	if req == nil {
		return nil, errors.New("openai: request must not be nil")
	}
	opts = append(opts, option.WithMiddleware(httpclient.ResponseBodyLimit(httpclient.DefaultMaxJSONResponseBytes).Apply))
	return a.wrapResult(a.images.Generate(ctx, *req, opts...))
}

func (a *api) moderation(ctx context.Context, req *openai.ModerationNewParams, opts ...option.RequestOption) (*openai.ModerationNewResponse, error) {
	if req == nil {
		return nil, errors.New("openai: request must not be nil")
	}
	opts = append(opts, option.WithMiddleware(httpclient.ResponseBodyLimit(httpclient.DefaultMaxJSONResponseBytes).Apply))
	return a.wrapResult(a.moderations.New(ctx, *req, opts...))
}

func (a *api) speech(ctx context.Context, req *openai.AudioSpeechNewParams, opts ...option.RequestOption) (*http.Response, error) {
	if req == nil {
		return nil, errors.New("openai: request must not be nil")
	}
	opts = append(opts, option.WithMiddleware(httpclient.ResponseBodyLimit(httpclient.DefaultMaxJSONResponseBytes).ApplyErrors))
	return a.wrapResult(a.audio.Speech.New(ctx, *req, opts...))
}

func (a *api) transcription(ctx context.Context, req *openai.AudioTranscriptionNewParams, opts ...option.RequestOption) (*openai.AudioTranscriptionNewResponseUnion, error) {
	if req == nil {
		return nil, errors.New("openai: request must not be nil")
	}
	opts = append(opts, option.WithMiddleware(httpclient.ResponseBodyLimit(httpclient.DefaultMaxJSONResponseBytes).Apply))
	return a.wrapResult(a.audio.Transcriptions.New(ctx, *req, opts...))
}

func (a *api) translation(ctx context.Context, req *openai.AudioTranslationNewParams, opts ...option.RequestOption) (*openai.Translation, error) {
	if req == nil {
		return nil, errors.New("openai: request must not be nil")
	}
	opts = append(opts, option.WithMiddleware(httpclient.ResponseBodyLimit(httpclient.DefaultMaxJSONResponseBytes).Apply))
	// Compatible endpoints still expose synchronous multipart translation.
	// Realtime translation has a different transport and request lifecycle.
	client := openai.Client{Options: a.audio.Options}
	var response *openai.Translation
	err := client.Post(ctx, "audio/translations", *req, &response, opts...)
	return a.wrapResult(response, err)
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
