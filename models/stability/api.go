package stability

import (
	"cmp"
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"strconv"

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
		return errors.New("stability: APIKey is required")
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
		return nil, fmt.Errorf("stability: HTTP client: %w", err)
	}
	client := resty.NewWithClient(boundClient)
	client.SetJSONMarshaler(func(value any) ([]byte, error) { return jsonv2.Marshal(value) }).
		SetJSONUnmarshaler(func(data []byte, value any) error { return jsonv2.Unmarshal(data, value) })
	client.SetBaseURL(baseURL).
		SetAuthToken(config.APIKey)

	return &api{http: client}, nil
}

// generateRequest models the union of fields each v2beta image endpoint
// accepts. Mode selects the response wrapping: [ResponseModeImage] returns
// raw bytes; [ResponseModeJSON] returns a base64 envelope with FinishReason
// + Seed echoed back (required when callers care about those).
type generateRequest struct {
	Prompt         string   `json:"prompt,omitempty"`
	NegativePrompt string   `json:"negative_prompt,omitempty"`
	AspectRatio    string   `json:"aspect_ratio,omitempty"`
	Model          string   `json:"model,omitempty"`
	OutputFormat   string   `json:"output_format,omitempty"`
	Seed           *int64   `json:"seed,omitzero"`
	StylePreset    string   `json:"style_preset,omitempty"`
	CFGScale       *float64 `json:"cfg_scale,omitzero"`
	Mode           string   `json:"-"`
}

func (g *generateRequest) UnmarshalJSON(data []byte) error {
	type wireOptions generateRequest
	var decoded *wireOptions
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("stability: image options: %w", err)
	}
	if decoded == nil {
		return errors.New("stability: image options must be an object")
	}
	*g = generateRequest(*decoded)
	return nil
}

func (g *generateRequest) formFields() map[string]string {
	out := make(map[string]string)
	put := func(k, v string) {
		if v != "" {
			out[k] = v
		}
	}
	put(fieldPrompt, g.Prompt)
	put(fieldNegativePrompt, g.NegativePrompt)
	put(fieldAspectRatio, g.AspectRatio)
	put(fieldModel, g.Model)
	put(fieldOutputFormat, g.OutputFormat)
	put(fieldStylePreset, g.StylePreset)
	if g.CFGScale != nil {
		out[fieldCfgScale] = strconv.FormatFloat(*g.CFGScale, 'f', -1, 64)
	}
	if g.Seed != nil {
		out[fieldSeed] = strconv.FormatInt(*g.Seed, 10)
	}
	return out
}

func (g *generateRequest) validate() error {
	if g.AspectRatio != "" {
		switch g.AspectRatio {
		case aspectRatio16By9, aspectRatio1By1, aspectRatio21By9, aspectRatio2By3, aspectRatio3By2, aspectRatio4By5, aspectRatio5By4, aspectRatio9By16, aspectRatio9By21:
		default:
			return fmt.Errorf("stability: unsupported aspect_ratio %q", g.AspectRatio)
		}
	}
	if g.OutputFormat != "" && g.OutputFormat != outputFormatJPEG && g.OutputFormat != outputFormatPNG && g.OutputFormat != outputFormatWebP {
		return fmt.Errorf("stability: output_format must be jpeg, png, or webp, got %q", g.OutputFormat)
	}
	if g.Seed != nil && (*g.Seed < 0 || *g.Seed > maximumSeed) {
		return fmt.Errorf("stability: seed must be between 0 and 4294967294, got %d", *g.Seed)
	}
	if g.CFGScale != nil && (*g.CFGScale < minimumCFGScale || *g.CFGScale > maximumCFGScale) {
		return fmt.Errorf("stability: cfg_scale must be between 1 and 10, got %g", *g.CFGScale)
	}
	return nil
}

type jsonResponse struct {
	Image        string `json:"image"`
	FinishReason string `json:"finish_reason"`
	Seed         *int64 `json:"seed,omitzero"`
}

func (a *api) generate(ctx context.Context, path string, req *generateRequest) ([]byte, http.Header, error) {
	if req == nil {
		return nil, nil, errors.New("stability: request must not be nil")
	}

	r := a.http.R().
		SetContext(ctx).
		SetMultipartFormData(req.formFields()).
		SetHeader(headerAccept, cmp.Or(req.Mode, ResponseModeImage))

	resp, err := r.Post(path)
	if err != nil {
		return nil, nil, fmt.Errorf("stability: request failed: %w", err)
	}
	if !resp.IsSuccess() {
		return nil, nil, fmt.Errorf("stability: http %d: %s", resp.StatusCode(), resp.String())
	}
	return resp.Body(), resp.Header(), nil
}

func decodeJSON(body []byte) (*jsonResponse, error) {
	var resp jsonResponse
	if err := jsonv2.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("stability: decode json: %w", err)
	}
	return &resp, nil
}
