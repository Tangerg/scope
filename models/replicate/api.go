package replicate

import (
	"cmp"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/go-resty/resty/v2"

	"github.com/Tangerg/scope/core/httpclient"
	"github.com/Tangerg/scope/core/invocation"
)

const defaultMaxOutputBytes = 32 << 20

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
	policy   outputPolicy
	apiKey   string
}

type predictionRunner struct {
	api             *api
	pollInterval    time.Duration
	pollTimeout     time.Duration
	invalidResponse error
}

func newPredictionRunner(api *api, pollInterval, pollTimeout, defaultTimeout time.Duration, invalidResponse error) predictionRunner {
	if pollInterval == 0 {
		pollInterval = time.Duration(DefaultPollIntervalSeconds) * time.Second
	}
	if pollTimeout == 0 {
		pollTimeout = defaultTimeout
	}
	return predictionRunner{api: api, pollInterval: pollInterval, pollTimeout: pollTimeout, invalidResponse: invalidResponse}
}

func (p predictionRunner) run(ctx context.Context, model modelReference, request *predictionRequest) (*predictionResponse, error) {
	if err := request.validate(); err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}
	if err := ctx.Err(); err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}
	submitted, err := p.api.createPrediction(ctx, model, request)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureUnknown, Cause: err}
	}
	if submitted.ID == "" {
		return nil, &invocation.Failure{Kind: invocation.FailureUnknown, Cause: fmt.Errorf("%w: replicate: submitted prediction requires id", p.invalidResponse)}
	}
	response, err := p.poll(ctx, submitted)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureUnknown, Cause: err}
	}
	if response.Status != predictionStatusSucceeded {
		message := cmp.Or(response.Error, string(response.Status))
		return nil, &invocation.Failure{Kind: invocation.FailureFailed, Cause: fmt.Errorf("replicate: generation %s: %s", response.Status, message)}
	}
	return response, nil
}

func (p predictionRunner) poll(ctx context.Context, submitted *predictionResponse) (*predictionResponse, error) {
	deadline, cancel := context.WithTimeout(ctx, p.pollTimeout)
	defer cancel()

	ticker := time.NewTicker(p.pollInterval)
	defer ticker.Stop()

	response := submitted
	for {
		switch response.Status {
		case predictionStatusSucceeded, predictionStatusFailed, predictionStatusCanceled, predictionStatusAborted:
			return response, nil
		case predictionStatusStarting, predictionStatusProcessing:
		default:
			return nil, fmt.Errorf("replicate: prediction %s reports unrecognized status %q", submitted.ID, response.Status)
		}
		var err error
		response, err = p.api.getPrediction(deadline, submitted.ID)
		if err != nil {
			return nil, err
		}
		if response.ID != submitted.ID {
			return nil, fmt.Errorf("%w: replicate: polled prediction id %q differs from submitted id %q", p.invalidResponse, response.ID, submitted.ID)
		}
		if response.Status != predictionStatusStarting && response.Status != predictionStatusProcessing {
			continue
		}
		select {
		case <-deadline.Done():
			return nil, deadline.Err()
		case <-ticker.C:
		}
	}
}

func newAPI(ctx context.Context, config apiConfig) (*api, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	baseURL := cmp.Or(config.BaseURL, DefaultBaseURL)
	boundClient, err := httpclient.New(ctx, httpclient.Config{HTTPClient: config.HTTPClient, BaseURL: baseURL})
	if err != nil {
		return nil, fmt.Errorf("replicate: HTTP client: %w", err)
	}
	parsedBaseURL, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("replicate: parse BaseURL: %w", err)
	}
	client := resty.NewWithClient(boundClient).SetResponseBodyLimit(httpclient.DefaultMaxJSONResponseBytes)
	client.SetJSONMarshaler(func(value any) ([]byte, error) { return jsonv2.Marshal(value) }).
		SetJSONUnmarshaler(func(data []byte, value any) error { return jsonv2.Unmarshal(data, value) })
	client.SetBaseURL(baseURL).
		SetAuthToken(config.APIKey).
		SetHeader(headerContentType, mediaTypeJSON)
	origin, err := httpclient.NewOrigin(ctx, parsedBaseURL)
	if err != nil {
		return nil, fmt.Errorf("replicate: output origin: %w", err)
	}
	adapter := &api{http: client, policy: outputPolicy{apiOrigin: origin}, apiKey: config.APIKey}
	adapter.download = resty.NewWithClient(adapter.policy.client(config.HTTPClient)).SetResponseBodyLimit(defaultMaxOutputBytes)
	return adapter, nil
}

// The binding owns the model reference; extensions cannot select another version.
type predictionRequest struct {
	Input               map[string]json.RawMessage `json:"input"`
	Webhook             string                     `json:"webhook,omitempty"`
	WebhookEventsFilter []string                   `json:"webhook_events_filter,omitzero"`
}

func (p *predictionRequest) UnmarshalJSON(data []byte) error {
	type wireOptions predictionRequest
	var decoded *wireOptions
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("replicate: prediction options: %w", err)
	}
	if decoded == nil {
		return errors.New("replicate: prediction options must be an object")
	}
	*p = predictionRequest(*decoded)
	return nil
}

// setInput encodes a Core-owned value as its own JSON token and stores it in the
// native input map. Keeping each value as raw JSON makes the open input subtree a
// lossless representation, so a large integer never rounds through float64.
func (p *predictionRequest) setInput(key string, value any) error {
	raw, err := jsonv2.Marshal(value)
	if err != nil {
		return fmt.Errorf("replicate: encode input %q: %w", key, err)
	}
	if p.Input == nil {
		p.Input = map[string]json.RawMessage{}
	}
	p.Input[key] = raw
	return nil
}

func (p *predictionRequest) validate() error {
	if p == nil || p.Input == nil {
		return errors.New("replicate: prediction input is required")
	}
	if _, err := jsonv2.Marshal(p.Input); err != nil {
		return fmt.Errorf("replicate: prediction input is not valid JSON: %w", err)
	}
	if p.Webhook != "" {
		parsed, err := url.Parse(p.Webhook)
		if err != nil || parsed.Scheme != schemeHTTPS || parsed.Host == "" {
			return fmt.Errorf("replicate: webhook must be an absolute HTTPS URL: %q", p.Webhook)
		}
	}
	if len(p.WebhookEventsFilter) > 0 && p.Webhook == "" {
		return errors.New("replicate: webhook_events_filter requires webhook")
	}
	seenEvents := make(map[string]struct{}, len(p.WebhookEventsFilter))
	for index, event := range p.WebhookEventsFilter {
		switch event {
		case webhookEventStart, webhookEventOutput, webhookEventLogs, webhookEventCompleted:
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
	Input               map[string]json.RawMessage `json:"input"`
	Version             modelReference             `json:"version"`
	Webhook             string                     `json:"webhook,omitempty"`
	WebhookEventsFilter []string                   `json:"webhook_events_filter,omitzero"`
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
	ID          string                     `json:"id"`
	Model       string                     `json:"model,omitempty"`
	Version     string                     `json:"version,omitempty"`
	Status      predictionStatus           `json:"status"`
	Input       map[string]json.RawMessage `json:"input,omitzero"`
	Output      any                        `json:"output,omitzero"`
	Error       string                     `json:"error,omitempty"`
	Logs        string                     `json:"logs,omitempty"`
	CreatedAt   string                     `json:"created_at,omitempty"`
	StartedAt   string                     `json:"started_at,omitempty"`
	CompletedAt string                     `json:"completed_at,omitempty"`
	DataRemoved bool                       `json:"data_removed,omitzero"`
	Source      string                     `json:"source,omitempty"`
	URLs        struct {
		Get    string `json:"get"`
		Cancel string `json:"cancel"`
		Stream string `json:"stream,omitempty"`
	} `json:"urls"`
	Metrics struct {
		PredictTime float64 `json:"predict_time,omitzero"`
		TotalTime   float64 `json:"total_time,omitzero"`
	} `json:"metrics"`
}

func (p predictionResponse) predictTimeMilliseconds() (int64, bool) {
	if p.Metrics.PredictTime <= 0 {
		return 0, false
	}
	return int64(p.Metrics.PredictTime * float64(time.Second/time.Millisecond)), true
}

func (a *api) createPrediction(ctx context.Context, model modelReference, req *predictionRequest) (*predictionResponse, error) {
	body := predictionRequestBody{
		Input:               req.Input,
		Version:             model,
		Webhook:             req.Webhook,
		WebhookEventsFilter: req.WebhookEventsFilter,
	}

	var out predictionResponse
	resp, err := a.http.R().SetContext(ctx).SetBody(&body).SetResult(&out).Post(pathPredictions)
	if err != nil {
		return nil, fmt.Errorf("replicate: submit failed: %w", err)
	}
	if !resp.IsSuccess() {
		return nil, fmt.Errorf("replicate: %w", httpclient.NewResponseError(resp.StatusCode(), resp.Header()))
	}
	return &out, nil
}

func (a *api) downloadOutput(ctx context.Context, rawURL string) ([]byte, string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, "", fmt.Errorf("replicate: parse output URL: %w", err)
	}
	origin, err := a.policy.originFor(ctx, parsed)
	if err != nil {
		return nil, "", err
	}
	request := a.download.R().SetContext(ctx)
	if origin == a.policy.apiOrigin {
		request.SetAuthToken(a.apiKey)
	}
	response, err := request.Get(rawURL)
	if err != nil {
		return nil, "", fmt.Errorf("replicate: download output: %w", err)
	}
	if !response.IsSuccess() {
		return nil, "", fmt.Errorf("replicate: download output %w", httpclient.NewResponseError(response.StatusCode(), response.Header()))
	}
	if len(response.Body()) == 0 {
		return nil, "", errors.New("replicate: downloaded output is empty")
	}
	return response.Body(), response.Header().Get(headerContentType), nil
}

// getPrediction polls a prediction's current state.
func (a *api) getPrediction(ctx context.Context, id string) (*predictionResponse, error) {
	if id == "" {
		return nil, errors.New("replicate: prediction id must not be empty")
	}
	var out predictionResponse
	resp, err := a.http.R().SetContext(ctx).SetResult(&out).Get(pathPredictionPrefix + url.PathEscape(id))
	if err != nil {
		return nil, fmt.Errorf("replicate: poll failed: %w", err)
	}
	if !resp.IsSuccess() {
		return nil, fmt.Errorf("replicate: %w", httpclient.NewResponseError(resp.StatusCode(), resp.Header()))
	}
	return &out, nil
}
