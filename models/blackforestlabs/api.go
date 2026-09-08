package blackforestlabs

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-resty/resty/v2"
)

type apiConfig struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

func (a apiConfig) validate() error {
	if a.APIKey == "" {
		return errors.New("blackforestlabs: APIKey is required")
	}
	return nil
}

type api struct {
	http     *resty.Client
	download *resty.Client
	baseURL  *url.URL
}

func newAPI(config apiConfig) (*api, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	baseURL := cmp.Or(config.BaseURL, DefaultBaseURL)
	parsedBaseURL, err := url.Parse(baseURL)
	if err != nil || parsedBaseURL.Scheme == "" || parsedBaseURL.Host == "" {
		return nil, fmt.Errorf("blackforestlabs: invalid BaseURL %q", baseURL)
	}

	client := resty.New()
	download := resty.New()
	if config.HTTPClient != nil {
		client = resty.NewWithClient(config.HTTPClient)
		download = resty.NewWithClient(config.HTTPClient)
	}
	client.SetBaseURL(baseURL).
		SetHeader("x-key", config.APIKey).
		SetHeader("Content-Type", "application/json")
	return &api{http: client, download: download, baseURL: parsedBaseURL}, nil
}

// GenerateRequest is the union of fields the various Flux endpoints
// accept. Each Flux model (flux-pro-1.1, flux-pro-1.1-ultra,
// flux-kontext-pro, ...) has its own endpoint path; all fields are
// forwarded and the API rejects unknown ones.
type generateRequest struct {
	Prompt           string `json:"prompt"`
	ImagePrompt      string `json:"image_prompt,omitempty"`
	InputImage       string `json:"input_image,omitempty"`
	InputImage2      string `json:"input_image_2,omitempty"`
	InputImage3      string `json:"input_image_3,omitempty"`
	InputImage4      string `json:"input_image_4,omitempty"`
	InputImage5      string `json:"input_image_5,omitempty"`
	InputImage6      string `json:"input_image_6,omitempty"`
	InputImage7      string `json:"input_image_7,omitempty"`
	InputImage8      string `json:"input_image_8,omitempty"`
	Width            int    `json:"width,omitempty"`
	Height           int    `json:"height,omitempty"`
	AspectRatio      string `json:"aspect_ratio,omitempty"`
	PromptUpsampling bool   `json:"prompt_upsampling,omitzero"`
	DisablePUP       bool   `json:"disable_pup,omitzero"`
	Seed             *int64 `json:"seed,omitempty"`
	SafetyTolerance  *int   `json:"safety_tolerance,omitempty"`
	OutputFormat     string `json:"output_format,omitempty"`
	Raw              bool   `json:"raw,omitzero"`
	WebhookURL       string `json:"webhook_url,omitempty"`
	WebhookSecret    string `json:"webhook_secret,omitempty"`
}

// AsyncResponse is the body of any POST /v1/<model> call — it returns a
// task id which the caller polls via GetResult.
type asyncResponse struct {
	ID         string   `json:"id"`
	PollingURL string   `json:"polling_url"`
	Cost       *float64 `json:"cost,omitempty"`
	InputMP    *float64 `json:"input_mp,omitempty"`
	OutputMP   *float64 `json:"output_mp,omitempty"`
}

// pollStatus enumerates the statuses BFL's get_result reference declares.
// Pending, Reasoning, and Generating are the three still-running states; every
// other value ends the job.
type pollStatus string

const (
	pollStatusPending    pollStatus = "Pending"
	pollStatusReasoning  pollStatus = "Reasoning"
	pollStatusGenerating pollStatus = "Generating"

	pollStatusReady pollStatus = "Ready"

	pollStatusError            pollStatus = "Error"
	pollStatusRequestModerated pollStatus = "Request Moderated"
	pollStatusContentModerated pollStatus = "Content Moderated"
	pollStatusTaskNotFound     pollStatus = "Task not found"

	// pollStatusFailed is absent from the get_result enum but is one of the
	// two values BFL's own quick-start polling loop breaks out of, so it is
	// treated as the end state that example says it is.
	pollStatusFailed pollStatus = "Failed"
)

// PollResult is the body of GET /v1/get_result?id=...
type pollResult struct {
	ID       string         `json:"id"`
	Status   pollStatus     `json:"status"`
	Progress *float64       `json:"progress,omitempty"`
	Details  map[string]any `json:"details,omitempty"`
	Preview  map[string]any `json:"preview,omitempty"`
	Result   struct {
		Sample   string `json:"sample"`
		Prompt   string `json:"prompt"`
		Seed     int64  `json:"seed"`
		Duration int64  `json:"duration"`
	} `json:"result"`
}

// generate posts to /<model> (e.g. "flux-pro-1.1", "flux-kontext-pro").
func (a *api) generate(ctx context.Context, model string, req *generateRequest) (*asyncResponse, error) {
	if req == nil {
		return nil, errors.New("blackforestlabs: request must not be nil")
	}
	var out asyncResponse
	resp, err := a.http.R().SetContext(ctx).SetBody(req).SetResult(&out).Post("/" + model)
	if err != nil {
		return nil, fmt.Errorf("blackforestlabs: request failed: %w", err)
	}
	if !resp.IsSuccess() {
		return nil, fmt.Errorf("blackforestlabs: http %d: %s", resp.StatusCode(), resp.String())
	}
	return &out, nil
}

// getResult fetches the current task state through the exact polling URL
// returned by the generation endpoint.
func (a *api) getResult(ctx context.Context, pollingURL string) (*pollResult, error) {
	if err := a.validateProviderURL(pollingURL); err != nil {
		return nil, fmt.Errorf("blackforestlabs: invalid polling URL: %w", err)
	}
	var out pollResult
	resp, err := a.http.R().SetContext(ctx).SetResult(&out).Get(pollingURL)
	if err != nil {
		return nil, fmt.Errorf("blackforestlabs: poll failed: %w", err)
	}
	if !resp.IsSuccess() {
		return nil, fmt.Errorf("blackforestlabs: http %d: %s", resp.StatusCode(), resp.String())
	}
	return &out, nil
}

// downloadOutput retrieves a short-lived BFL delivery URL without forwarding
// the x-key credential to the delivery host.
func (a *api) downloadOutput(ctx context.Context, outputURL string) ([]byte, string, error) {
	if err := a.validateProviderURL(outputURL); err != nil {
		return nil, "", fmt.Errorf("blackforestlabs: invalid output URL: %w", err)
	}
	response, err := a.download.R().SetContext(ctx).Get(outputURL)
	if err != nil {
		return nil, "", fmt.Errorf("blackforestlabs: download output: %w", err)
	}
	if !response.IsSuccess() {
		return nil, "", fmt.Errorf("blackforestlabs: download output: http %d: %s", response.StatusCode(), response.String())
	}
	if len(response.Body()) == 0 {
		return nil, "", errors.New("blackforestlabs: download output returned an empty body")
	}
	return response.Body(), response.Header().Get("Content-Type"), nil
}

func (a *api) validateProviderURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("URL must be absolute: %q", rawURL)
	}
	if strings.EqualFold(parsed.Host, a.baseURL.Host) && parsed.Scheme == a.baseURL.Scheme {
		return nil
	}
	hostname := strings.ToLower(parsed.Hostname())
	if parsed.Scheme != "https" || (hostname != "bfl.ai" && !strings.HasSuffix(hostname, ".bfl.ai")) {
		return fmt.Errorf("URL host is not an official BFL endpoint: %q", rawURL)
	}
	return nil
}
