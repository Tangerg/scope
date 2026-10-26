package blackforestlabs

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
)

type ImageModelConfig struct {
	APIKey         string
	DefaultOptions image.Options
	BaseURL        string
	HTTPClient     *http.Client

	// PollInterval / PollTimeout configure the synchronous wrapper
	// around BFL's async generation. Zero values fall back to the
	// package defaults.
	PollInterval time.Duration
	PollTimeout  time.Duration
}

func (i ImageModelConfig) Validate() error {
	if i.APIKey == "" {
		return errors.New("blackforestlabs: APIKey is required")
	}
	if i.DefaultOptions.Model == "" {
		return errors.New("blackforestlabs: DefaultOptions.Model is required")
	}
	if err := i.DefaultOptions.Validate(); err != nil {
		return err
	}
	if i.PollInterval < 0 {
		return errors.New("blackforestlabs: PollInterval must not be negative")
	}
	if i.PollTimeout < 0 {
		return errors.New("blackforestlabs: PollTimeout must not be negative")
	}
	return nil
}

var _ image.Model = (*ImageModel)(nil)

// ImageModel wraps Black Forest Labs' Flux image-generation endpoints.
// Model id picks the engine ("flux-pro-1.1", "flux-pro-1.1-ultra",
// "flux-dev", "flux-kontext-pro", "flux-kontext-max"). BFL is async
// only — Call submits + polls until ready.
type ImageModel struct {
	api            *api
	defaultOptions image.Options
	pollInterval   time.Duration
	pollTimeout    time.Duration
}

func NewImageModel(ctx context.Context, config ImageModelConfig) (*ImageModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	api, err := newAPI(ctx, apiConfig{
		APIKey:     config.APIKey,
		BaseURL:    config.BaseURL,
		HTTPClient: config.HTTPClient,
	})
	if err != nil {
		return nil, err
	}
	pi := config.PollInterval
	if pi == 0 {
		pi = time.Duration(DefaultPollIntervalSeconds) * time.Second
	}
	pt := config.PollTimeout
	if pt == 0 {
		pt = time.Duration(DefaultPollTimeoutSeconds) * time.Second
	}
	return &ImageModel{api: api, defaultOptions: config.DefaultOptions.Clone(), pollInterval: pi, pollTimeout: pt}, nil
}

func (i *ImageModel) Call(ctx context.Context, req *image.Request) (*image.Response, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	model, apiReq, err := i.buildRequest(req)
	if err != nil {
		return nil, err
	}
	async, final, err := i.runGeneration(ctx, model, apiReq)
	if err != nil {
		return nil, err
	}
	value, err := i.downloadGeneratedImage(ctx, final.Result.Sample, apiReq.OutputFormat)
	if err != nil {
		return nil, err
	}
	return mapImageResponse(value, async, final)
}

func (i *ImageModel) buildRequest(req *image.Request) (string, *generateRequest, error) {
	effectiveOptions, err := i.defaultOptions.Resolve(req.Options)
	if err != nil {
		return "", nil, err
	}
	if effectiveOptions.NegativePrompt != "" {
		return "", nil, errors.New("blackforestlabs: image: unsupported option: negative_prompt")
	}

	raw, found, decodeErr := effectiveOptions.Extensions.Decode[json.RawMessage](ImageRequestExtensionKey)
	if decodeErr != nil {
		return "", nil, decodeErr
	}
	var nativeFields map[string]json.RawMessage
	if found {
		if err := jsonv2.Unmarshal(raw, &nativeFields); err != nil {
			return "", nil, err
		}
	}
	for _, field := range []string{"prompt", "width", "height", "seed", "output_format"} {
		if _, exists := nativeFields[field]; exists {
			return "", nil, fmt.Errorf("blackforestlabs: extension %q field %q is owned by Core", ImageRequestExtensionKey, field)
		}
	}

	apiReq := &generateRequest{}
	if found {
		if nativeFields == nil {
			return "", nil, errors.New("blackforestlabs: image request extension must be an object")
		}
		if err := jsonv2.Unmarshal(raw, apiReq, jsonv2.RejectUnknownMembers(true)); err != nil {
			return "", nil, err
		}
	}
	apiReq.Prompt = req.Prompt
	if effectiveOptions.Width != nil {
		apiReq.Width = int(*effectiveOptions.Width)
		if int64(apiReq.Width) != *effectiveOptions.Width {
			return "", nil, fmt.Errorf("blackforestlabs: image: width %d exceeds int", *effectiveOptions.Width)
		}
	}
	if effectiveOptions.Height != nil {
		apiReq.Height = int(*effectiveOptions.Height)
		if int64(apiReq.Height) != *effectiveOptions.Height {
			return "", nil, fmt.Errorf("blackforestlabs: image: height %d exceeds int", *effectiveOptions.Height)
		}
	}
	if effectiveOptions.Seed != nil {
		apiReq.Seed = effectiveOptions.Seed
	}
	if effectiveOptions.OutputFormat != "" {
		apiReq.OutputFormat = strings.TrimPrefix(effectiveOptions.OutputFormat, "image/")
	}
	return effectiveOptions.Model, apiReq, nil
}

func (i *ImageModel) runGeneration(ctx context.Context, model string, request *generateRequest) (*asyncResponse, *pollResult, error) {
	async, err := i.api.generate(ctx, model, request)
	if err != nil {
		return nil, nil, err
	}
	if async.ID == "" {
		return nil, nil, fmt.Errorf("%w: blackforestlabs: generation response has no task id", image.ErrInvalidResponse)
	}
	if async.PollingURL == "" {
		return nil, nil, errors.New("blackforestlabs: generation response has no polling_url")
	}
	final, err := i.pollUntilDone(ctx, async)
	if err != nil {
		return nil, nil, err
	}
	if final.Result.Sample == "" {
		return nil, nil, errors.New("blackforestlabs: ready output has no sample URL")
	}
	return async, final, nil
}

func (i *ImageModel) downloadGeneratedImage(ctx context.Context, outputURL, outputFormat string) (*media.Media, error) {
	data, mimeType, err := i.api.downloadOutput(ctx, outputURL)
	if err != nil {
		return nil, err
	}
	if mimeType == "" {
		mimeType = mime.TypeByExtension("." + outputFormat)
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return media.NewBytes(mimeType, data)
}

func mapImageResponse(value *media.Media, async *asyncResponse, final *pollResult) (*image.Response, error) {
	var outputMetadata metadata.Map
	if setErr := outputMetadata.Set("blackforestlabs/output_url", final.Result.Sample); setErr != nil {
		return nil, setErr
	}
	if final.Result.Seed != nil {
		if setErr := outputMetadata.Set("blackforestlabs/seed", *final.Result.Seed); setErr != nil {
			return nil, setErr
		}
	}
	if final.Result.Duration != nil {
		if setErr := outputMetadata.Set("blackforestlabs/duration_ms", *final.Result.Duration); setErr != nil {
			return nil, setErr
		}
	}

	output, err := image.NewOutput(value, outputMetadata)
	if err != nil {
		return nil, err
	}

	meta := &image.ResponseMetadata{}
	if err := meta.Extra.Set("blackforestlabs/task_id", async.ID); err != nil {
		return nil, err
	}
	if err := meta.Extra.Set("blackforestlabs/submit_response", async); err != nil {
		return nil, err
	}
	if err := meta.Extra.Set("blackforestlabs/result_response", final); err != nil {
		return nil, err
	}
	return image.NewResponse([]*image.Output{output}, meta)
}

func (i *ImageModel) pollUntilDone(ctx context.Context, submitted *asyncResponse) (*pollResult, error) {
	deadline, cancel := context.WithTimeout(ctx, i.pollTimeout)
	defer cancel()

	ticker := time.NewTicker(i.pollInterval)
	defer ticker.Stop()

	for {
		resp, err := i.api.getResult(deadline, submitted.PollingURL)
		if err != nil {
			return nil, err
		}
		if resp.ID != submitted.ID {
			return nil, fmt.Errorf("%w: blackforestlabs: polled task id %q differs from submitted id %q", image.ErrInvalidResponse, resp.ID, submitted.ID)
		}
		switch resp.Status {
		case pollStatusReady:
			return resp, nil
		case pollStatusPending, pollStatusReasoning, pollStatusGenerating:
			// The three states BFL documents as still running.
		case pollStatusError, pollStatusFailed, pollStatusContentModerated, pollStatusRequestModerated, pollStatusTaskNotFound:
			return nil, fmt.Errorf("blackforestlabs: generation failed: %s", resp.Status)
		default:
			// Continuing to poll is only safe for a state known to advance, so
			// an unrecognized one is reported rather than absorbed: an added
			// end state would otherwise arrive as this call's own timeout,
			// whose obvious remedies are both wrong.
			return nil, fmt.Errorf("blackforestlabs: generation reports unrecognized status %q", resp.Status)
		}
		select {
		case <-deadline.Done():
			return nil, deadline.Err()
		case <-ticker.C:
		}
	}
}
