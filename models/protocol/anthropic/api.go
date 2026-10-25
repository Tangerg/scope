package anthropic

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/Tangerg/scope/core/httpclient"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"
)

const (
	defaultAPIBaseURL            = "https://api.anthropic.com/"
	defaultResponseHeaderTimeout = 10 * time.Minute
)

type apiConfig struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
	Headers    http.Header
}

func (a apiConfig) validate() error {
	_, err := a.endpoint()
	return err
}

func (a apiConfig) endpoint() (string, error) {
	if a.APIKey == "" {
		return "", errors.New("anthropic: APIKey is required")
	}
	baseURL, source, present := a.BaseURL, "BaseURL", a.BaseURL != ""
	if !present {
		baseURL, present = os.LookupEnv("ANTHROPIC_BASE_URL")
		source = "ANTHROPIC_BASE_URL"
	}
	if !present {
		baseURL = defaultAPIBaseURL
	}
	endpoint, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("anthropic: %s: %w", source, err)
	}
	if (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Hostname() == "" || endpoint.User != nil {
		return "", fmt.Errorf("anthropic: %s must be an absolute HTTP or HTTPS URL with a host", source)
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
	options := []option.RequestOption{option.WithoutEnvironmentDefaults(), option.WithAPIKey(a.APIKey)}
	options = append(options, option.WithBaseURL(baseURL), option.WithHTTPClient(client))
	for name, values := range a.Headers {
		if len(values) == 0 {
			options = append(options, option.WithHeaderDel(name))
			continue
		}
		for _, value := range values {
			options = append(options, option.WithHeader(name, value))
		}
	}
	return options, nil
}

// WithoutEnvironmentDefaults also omits the SDK's HTTP default. Restore its
// header deadline without timing out an otherwise healthy streaming body.
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
	client *anthropicsdk.Client
}

func newAPI(ctx context.Context, config apiConfig) (*api, error) {
	options, err := config.requestOptions(ctx)
	if err != nil {
		return nil, err
	}
	client := anthropicsdk.NewClient(options...)
	return &api{client: &client}, nil
}

func (a *api) listModels(ctx context.Context, maxModels int, maxResponseBytes int64) ([]string, error) {
	if maxModels <= 0 || maxResponseBytes <= 0 {
		return nil, errors.New("anthropic: model count and response byte limits must be positive")
	}
	const maximumPageSize = 1000
	budget := modelListResponseBudget{remaining: maxResponseBytes}
	page, err := a.client.Models.List(ctx,
		anthropicsdk.ModelListParams{Limit: anthropicsdk.Int(int64(min(maxModels, maximumPageSize)))},
		option.WithMaxRetries(0), option.WithMiddleware(budget.read),
	)
	var ids []string
	cursors := make(map[string]struct{})
	for {
		if err != nil {
			return nil, a.wrapError(err)
		}
		if page == nil || !page.JSON.Data.Valid() {
			return nil, errors.New("anthropic: model list response must contain a data array")
		}
		switch jsontext.Value(page.JSON.HasMore.Raw()).Kind() {
		case 't', 'f':
		default:
			return nil, errors.New("anthropic: model list response must contain a boolean has_more")
		}
		if len(page.Data) > maxModels-len(ids) {
			return nil, fmt.Errorf("anthropic: model list exceeds %d-model limit", maxModels)
		}
		for _, model := range page.Data {
			if !model.JSON.ID.Valid() || jsontext.Value(model.JSON.ID.Raw()).Kind() != '"' || model.ID == "" {
				return nil, errors.New("anthropic: model list response contains an invalid model ID")
			}
			ids = append(ids, model.ID)
		}
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		if !page.HasMore {
			return ids, nil
		}
		if len(page.Data) == 0 || !page.JSON.LastID.Valid() || jsontext.Value(page.JSON.LastID.Raw()).Kind() != '"' || page.LastID == "" {
			return nil, errors.New("anthropic: incomplete model list has no next-page cursor")
		}
		if _, repeated := cursors[page.LastID]; repeated {
			return nil, errors.New("anthropic: model list pagination repeated a cursor")
		}
		if len(ids) == maxModels {
			return nil, fmt.Errorf("anthropic: incomplete model list reached %d-model limit", maxModels)
		}
		cursors[page.LastID] = struct{}{}
		page, err = page.GetNextPage()
	}
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
	response, err := a.wrapResult(a.client.Messages.CountTokens(ctx, *req, opts...))
	if err != nil {
		return nil, err
	}
	if response == nil || !response.JSON.InputTokens.Valid() || response.InputTokens < 0 {
		return nil, errors.New("anthropic: invalid input_tokens count")
	}
	return response, nil
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
