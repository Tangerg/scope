package prodia

import (
	"cmp"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-resty/resty/v2"

	"github.com/Tangerg/scope/core/httpclient"
)

type apiConfig struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

func (a apiConfig) validate() error {
	if a.APIKey == "" {
		return errors.New("prodia: APIKey is required")
	}
	return nil
}

type api struct {
	http *resty.Client
}

func newAPI(ctx context.Context, config apiConfig) (*api, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	baseURL := cmp.Or(config.BaseURL, DefaultBaseURL)
	boundClient, err := httpclient.New(ctx, httpclient.Config{HTTPClient: config.HTTPClient, BaseURL: baseURL})
	if err != nil {
		return nil, fmt.Errorf("prodia: HTTP client: %w", err)
	}
	client := resty.NewWithClient(boundClient)
	client.SetJSONMarshaler(func(value any) ([]byte, error) { return jsonv2.Marshal(value) }).
		SetJSONUnmarshaler(func(data []byte, value any) error { return jsonv2.Unmarshal(data, value) })
	client.SetBaseURL(baseURL).
		SetHeader(headerAuthorization, authorizationBearerPrefix+config.APIKey)
	return &api{http: client}, nil
}

type jobRequest struct {
	Type   string                     `json:"type"`
	Config map[string]json.RawMessage `json:"config"`
}

// setConfig encodes a Core-owned value as its own JSON token and stores it in the
// native config map. Keeping each value as raw JSON makes the open config subtree a
// lossless representation, so a large integer never rounds through float64.
func (j *jobRequest) setConfig(field string, value any) error {
	raw, err := jsonv2.Marshal(value)
	if err != nil {
		return fmt.Errorf("prodia: encode config %q: %w", field, err)
	}
	if j.Config == nil {
		j.Config = map[string]json.RawMessage{}
	}
	j.Config[field] = raw
	return nil
}

// Prodia's synchronous endpoint returns binary output when Accept selects an
// image media type; the model's open config remains a lossless JSON subtree.
func (a *api) job(ctx context.Context, req *jobRequest, accept string) ([]byte, http.Header, error) {
	if req == nil {
		return nil, nil, errors.New("prodia: request must not be nil")
	}
	resp, err := a.http.R().
		SetContext(ctx).
		SetHeader(headerContentType, mediaTypeJSON).
		SetHeader(headerAccept, cmp.Or(accept, mediaTypeImage)).
		SetBody(req).
		Post(pathJob)
	if err != nil {
		return nil, nil, fmt.Errorf("prodia: request failed: %w", err)
	}
	if !resp.IsSuccess() {
		return nil, nil, fmt.Errorf("prodia: http %d: %s", resp.StatusCode(), resp.String())
	}
	return resp.Body(), resp.Header(), nil
}
