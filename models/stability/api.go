package stability

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-resty/resty/v2"
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

func newAPI(config apiConfig) (*api, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}

	client := resty.New()
	if config.HTTPClient != nil {
		client = resty.NewWithClient(config.HTTPClient)
	}
	client.SetBaseURL(cmp.Or(config.BaseURL, DefaultBaseURL)).
		SetAuthToken(config.APIKey)

	return &api{http: client}, nil
}

// generateRequest models the union of fields each v2beta image endpoint
// accepts. Mode selects the response wrapping: [ResponseModeImage] returns
// raw bytes; [ResponseModeJSON] returns a base64 envelope with FinishReason
// + Seed echoed back (required when callers care about those).
type generateRequest struct {
	Prompt         string
	NegativePrompt string
	AspectRatio    string
	Model          string
	OutputFormat   string
	Seed           *int64
	StylePreset    string
	CFGScale       *float64
	Mode           string
}

func (g *generateRequest) formFields() map[string]string {
	out := make(map[string]string)
	put := func(k, v string) {
		if v != "" {
			out[k] = v
		}
	}
	put("prompt", g.Prompt)
	put("negative_prompt", g.NegativePrompt)
	put("aspect_ratio", g.AspectRatio)
	put("model", g.Model)
	put("output_format", g.OutputFormat)
	put("style_preset", g.StylePreset)
	if g.CFGScale != nil {
		out["cfg_scale"] = strconv.FormatFloat(*g.CFGScale, 'f', -1, 64)
	}
	if g.Seed != nil {
		out["seed"] = strconv.FormatInt(*g.Seed, 10)
	}
	return out
}

func (g *generateRequest) validate() error {
	if g.AspectRatio != "" {
		switch g.AspectRatio {
		case "16:9", "1:1", "21:9", "2:3", "3:2", "4:5", "5:4", "9:16", "9:21":
		default:
			return fmt.Errorf("stability: unsupported aspect_ratio %q", g.AspectRatio)
		}
	}
	if g.OutputFormat != "" && g.OutputFormat != "jpeg" && g.OutputFormat != "png" && g.OutputFormat != "webp" {
		return fmt.Errorf("stability: output_format must be jpeg, png, or webp, got %q", g.OutputFormat)
	}
	if g.Seed != nil && (*g.Seed < 0 || *g.Seed > 4294967294) {
		return fmt.Errorf("stability: seed must be between 0 and 4294967294, got %d", *g.Seed)
	}
	if g.CFGScale != nil && (*g.CFGScale < 1 || *g.CFGScale > 10) {
		return fmt.Errorf("stability: cfg_scale must be between 1 and 10, got %g", *g.CFGScale)
	}
	return nil
}

type jsonResponse struct {
	Image        string `json:"image"`
	FinishReason string `json:"finish_reason"`
	Seed         int64  `json:"seed"`
}

func (a *api) generate(ctx context.Context, path string, req *generateRequest) ([]byte, http.Header, error) {
	if req == nil {
		return nil, nil, errors.New("stability: request must not be nil")
	}

	r := a.http.R().
		SetContext(ctx).
		SetMultipartFormData(req.formFields()).
		SetHeader("Accept", cmp.Or(req.Mode, ResponseModeImage))

	resp, err := r.Post(path)
	if err != nil {
		return nil, nil, fmt.Errorf("stability: request failed: %w", err)
	}
	if !resp.IsSuccess() {
		return nil, nil, fmt.Errorf("stability: http %d: %s", resp.StatusCode(), resp.String())
	}
	return resp.Body(), resp.Header(), nil
}

// decodeJSON decodes Stability's JSON image envelope.
func decodeJSON(body []byte) (*jsonResponse, error) {
	var resp jsonResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("stability: decode json: %w", err)
	}
	return &resp, nil
}
