package replicate

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
)

type apiConfig struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

func (a apiConfig) validate() error {
	if a.APIKey == "" {
		return errors.New("replicate: APIKey is required")
	}
	return nil
}

type api struct {
	http     *resty.Client
	download *resty.Client
	baseHost string
}

type predictionRunner struct {
	api          *api
	pollInterval time.Duration
	pollTimeout  time.Duration
}

func newPredictionRunner(api *api, pollInterval, pollTimeout, defaultTimeout time.Duration) predictionRunner {
	if pollInterval == 0 {
		pollInterval = time.Duration(DefaultPollIntervalSeconds) * time.Second
	}
	if pollTimeout == 0 {
		pollTimeout = defaultTimeout
	}
	return predictionRunner{api: api, pollInterval: pollInterval, pollTimeout: pollTimeout}
}

func (p predictionRunner) run(ctx context.Context, model string, request *predictionRequest) (*predictionResponse, error) {
	submitted, err := p.api.createPrediction(ctx, model, request)
	if err != nil {
		return nil, err
	}
	return p.poll(ctx, submitted.ID)
}

func (p predictionRunner) poll(ctx context.Context, id string) (*predictionResponse, error) {
	deadline, cancel := context.WithTimeout(ctx, p.pollTimeout)
	defer cancel()

	ticker := time.NewTicker(p.pollInterval)
	defer ticker.Stop()

	for {
		response, err := p.api.getPrediction(deadline, id)
		if err != nil {
			return nil, err
		}
		switch response.Status {
		case predictionStatusSucceeded:
			return response, nil
		case predictionStatusStarting, predictionStatusProcessing:
			// The only two states Replicate documents as still running.
		case predictionStatusFailed, predictionStatusCanceled, predictionStatusAborted:
			// Waiting out the poll deadline here would report a permanent
			// provider verdict as a local timeout, and the caller's natural
			// remedy — retry, or raise the timeout — would be the wrong one.
			message := response.Error
			if message == "" {
				message = string(response.Status)
			}
			return nil, fmt.Errorf("replicate: generation %s: %s", response.Status, message)
		default:
			// Continuing to poll is only safe for a state known to advance, so
			// an unrecognized one is reported rather than absorbed: an added
			// end state would otherwise be indistinguishable from slowness.
			return nil, fmt.Errorf("replicate: prediction %s reports unrecognized status %q", id, response.Status)
		}
		select {
		case <-deadline.Done():
			return nil, deadline.Err()
		case <-ticker.C:
		}
	}
}

func newAPI(config apiConfig) (*api, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	client := resty.New()
	download := resty.New()
	if config.HTTPClient != nil {
		client = resty.NewWithClient(config.HTTPClient)
		download = resty.NewWithClient(config.HTTPClient)
	}
	baseURL := cmp.Or(config.BaseURL, DefaultBaseURL)
	parsedBaseURL, err := url.Parse(baseURL)
	if err != nil || parsedBaseURL.Scheme == "" || parsedBaseURL.Host == "" {
		return nil, fmt.Errorf("replicate: invalid BaseURL %q", baseURL)
	}
	client.SetBaseURL(baseURL).
		SetAuthToken(config.APIKey).
		SetHeader("Content-Type", "application/json")
	download.SetAuthToken(config.APIKey)
	return &api{http: client, download: download, baseHost: parsedBaseURL.Hostname()}, nil
}

// predictionRequest contains the caller-controlled body fields shared by both
// prediction endpoints. createPrediction derives version exclusively from the
// model id so endpoint routing and the immutable version cannot disagree.
type predictionRequest struct {
	Input               map[string]any `json:"input"`
	Webhook             string         `json:"webhook,omitempty"`
	WebhookEventsFilter []string       `json:"webhook_events_filter,omitzero"`
}

func (p predictionRequest) validate() error {
	if p.Input == nil {
		return errors.New("replicate: prediction input is required")
	}
	if _, err := json.Marshal(p.Input); err != nil {
		return fmt.Errorf("replicate: prediction input is not valid JSON: %w", err)
	}
	if p.Webhook != "" {
		parsed, err := url.Parse(p.Webhook)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			return fmt.Errorf("replicate: webhook must be an absolute HTTPS URL: %q", p.Webhook)
		}
	}
	if len(p.WebhookEventsFilter) > 0 && p.Webhook == "" {
		return errors.New("replicate: webhook_events_filter requires webhook")
	}
	seenEvents := make(map[string]struct{}, len(p.WebhookEventsFilter))
	for index, event := range p.WebhookEventsFilter {
		switch event {
		case "start", "output", "logs", "completed":
		default:
			return fmt.Errorf("replicate: webhook_events_filter[%d]: unsupported event %q", index, event)
		}
		if _, duplicate := seenEvents[event]; duplicate {
			return fmt.Errorf("replicate: webhook_events_filter[%d]: duplicate event %q", index, event)
		}
		seenEvents[event] = struct{}{}
	}
	return nil
}

type predictionRequestBody struct {
	Input               map[string]any `json:"input"`
	Version             string         `json:"version,omitempty"`
	Webhook             string         `json:"webhook,omitempty"`
	WebhookEventsFilter []string       `json:"webhook_events_filter,omitzero"`
}

// predictionStatus enumerates the six statuses Replicate's prediction
// lifecycle documents. Only starting and processing are still running; the
// rest are end states.
type predictionStatus string

const (
	predictionStatusStarting   predictionStatus = "starting"
	predictionStatusProcessing predictionStatus = "processing"
	predictionStatusSucceeded  predictionStatus = "succeeded"
	predictionStatusFailed     predictionStatus = "failed"
	predictionStatusCanceled   predictionStatus = "canceled"

	// predictionStatusAborted is reached when "the prediction exceeded its
	// deadline before it could start running", so it is never charged and
	// never advances.
	predictionStatusAborted predictionStatus = "aborted"
)

// PredictionResponse mirrors Replicate's prediction-job document. Output is
// model-specific: image models usually return []string (URLs) or a single
// string (URL); text models return []string (token chunks) or string.
type predictionResponse struct {
	ID          string           `json:"id"`
	Model       string           `json:"model,omitempty"`
	Version     string           `json:"version,omitempty"`
	Status      predictionStatus `json:"status"`
	Input       map[string]any   `json:"input,omitzero"`
	Output      any              `json:"output,omitempty"`
	Error       string           `json:"error,omitempty"`
	Logs        string           `json:"logs,omitempty"`
	CreatedAt   string           `json:"created_at,omitempty"`
	StartedAt   string           `json:"started_at,omitempty"`
	CompletedAt string           `json:"completed_at,omitempty"`
	DataRemoved bool             `json:"data_removed,omitempty"`
	Source      string           `json:"source,omitempty"`
	URLs        struct {
		Get    string `json:"get"`
		Cancel string `json:"cancel"`
		Stream string `json:"stream,omitempty"`
	} `json:"urls"`
	Metrics struct {
		PredictTime float64 `json:"predict_time,omitempty"`
		TotalTime   float64 `json:"total_time,omitempty"`
	} `json:"metrics"`
}

func (p predictionResponse) predictTimeMilliseconds() (int64, bool) {
	if p.Metrics.PredictTime <= 0 {
		return 0, false
	}
	return int64(p.Metrics.PredictTime * float64(time.Second/time.Millisecond)), true
}

// createPrediction submits a generation job. model accepts either
// "owner/name" (official model — routes to /v1/models/.../predictions)
// or "owner/name:version_hash" (community model — routes to
// /v1/predictions with version in body). The hash form lets callers
// pin to a specific community-uploaded snapshot.
func (a *api) createPrediction(ctx context.Context, modelID string, req *predictionRequest) (*predictionResponse, error) {
	if req == nil {
		return nil, errors.New("replicate: request must not be nil")
	}
	if err := req.validate(); err != nil {
		return nil, err
	}
	if modelID == "" {
		return nil, errors.New("replicate: model id must not be empty")
	}

	owner, name, version := parseModelID(modelID)
	if owner == "" || name == "" {
		return nil, fmt.Errorf("replicate: invalid model id %q (want owner/name[:version])", modelID)
	}

	var (
		path string
		body = predictionRequestBody{
			Input:               req.Input,
			Webhook:             req.Webhook,
			WebhookEventsFilter: req.WebhookEventsFilter,
		}
	)
	if version != "" {
		path = "/predictions"
		body.Version = modelID
	} else {
		path = fmt.Sprintf("/models/%s/%s/predictions", owner, name)
	}

	var out predictionResponse
	resp, err := a.http.R().SetContext(ctx).SetBody(&body).SetResult(&out).Post(path)
	if err != nil {
		return nil, fmt.Errorf("replicate: submit failed: %w", err)
	}
	if !resp.IsSuccess() {
		return nil, fmt.Errorf("replicate: http %d: %s", resp.StatusCode(), resp.String())
	}
	return &out, nil
}

func (a *api) downloadOutput(ctx context.Context, rawURL string) ([]byte, string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, "", fmt.Errorf("replicate: invalid output URL %q", rawURL)
	}
	host := parsed.Hostname()
	if parsed.Scheme != "https" && host != a.baseHost {
		return nil, "", fmt.Errorf("replicate: output URL must use HTTPS, got %q", parsed.Scheme)
	}
	if host != a.baseHost && host != "replicate.delivery" && !strings.HasSuffix(host, ".replicate.delivery") {
		return nil, "", fmt.Errorf("replicate: untrusted output host %q", host)
	}
	response, err := a.download.R().SetContext(ctx).Get(rawURL)
	if err != nil {
		return nil, "", fmt.Errorf("replicate: download output: %w", err)
	}
	if !response.IsSuccess() {
		return nil, "", fmt.Errorf("replicate: download output http %d: %s", response.StatusCode(), response.String())
	}
	if len(response.Body()) == 0 {
		return nil, "", errors.New("replicate: downloaded output is empty")
	}
	return response.Body(), response.Header().Get("Content-Type"), nil
}

// getPrediction polls a prediction's current state.
func (a *api) getPrediction(ctx context.Context, id string) (*predictionResponse, error) {
	if id == "" {
		return nil, errors.New("replicate: prediction id must not be empty")
	}
	var out predictionResponse
	resp, err := a.http.R().SetContext(ctx).SetResult(&out).Get("/predictions/" + id)
	if err != nil {
		return nil, fmt.Errorf("replicate: poll failed: %w", err)
	}
	if !resp.IsSuccess() {
		return nil, fmt.Errorf("replicate: http %d: %s", resp.StatusCode(), resp.String())
	}
	return &out, nil
}

// parseModelID splits "owner/name[:version]" into its parts. Empty
// returns indicate the input wasn't well-formed.
func parseModelID(id string) (owner, name, version string) {
	owner, rest, ok := strings.Cut(id, "/")
	if !ok || owner == "" || rest == "" || strings.Contains(rest, "/") {
		return "", "", ""
	}
	if n, v, hasVersion := strings.Cut(rest, ":"); hasVersion {
		if n == "" || v == "" || strings.Contains(v, ":") {
			return "", "", ""
		}
		return owner, n, v
	}
	return owner, rest, ""
}
