package anthropic

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"

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
	_, err := a.requestOptions()
	return err
}

func (a apiConfig) requestOptions() ([]option.RequestOption, error) {
	if a.APIKey == "" {
		return nil, errors.New("anthropic: APIKey is required")
	}
	environmentURL, environmentPresent := os.LookupEnv("ANTHROPIC_BASE_URL")
	for _, source := range []struct {
		name    string
		value   string
		present bool
	}{
		{"BaseURL", a.BaseURL, a.BaseURL != ""},
		{"ANTHROPIC_BASE_URL", environmentURL, environmentPresent},
	} {
		if !source.present {
			continue
		}
		endpoint, err := url.Parse(source.value)
		if err != nil {
			return nil, fmt.Errorf("anthropic: %s: %w", source.name, err)
		}
		if (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Hostname() == "" {
			return nil, fmt.Errorf("anthropic: %s must be an absolute HTTP or HTTPS URL with a host", source.name)
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

type api struct {
	client *anthropicsdk.Client
}

func newAPI(config apiConfig) (*api, error) {
	options, err := config.requestOptions()
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
