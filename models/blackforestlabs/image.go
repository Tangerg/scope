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

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
)

const (
	requestFieldPrompt       = "prompt"
	requestFieldWidth        = "width"
	requestFieldHeight       = "height"
	requestFieldSeed         = "seed"
	requestFieldOutputFormat = "output_format"

	outputURLMetadataKey      = Provider + "/output_url"
	seedMetadataKey           = Provider + "/seed"
	durationMetadataKey       = Provider + "/duration"
	taskIDMetadataKey         = Provider + "/task_id"
	submitResponseMetadataKey = Provider + "/submit_response"
	resultResponseMetadataKey = Provider + "/result_response"
)

type ImageModelConfig struct {
	APIKey         string
	DefaultOptions image.Options
	BaseURL        string
	HTTPClient     *http.Client
	OutputOrigins  []string

	// Polling bounds the wait for BFL's async generation; zero fields
	// select DefaultPollInterval and DefaultPollTimeout.
	Polling invocation.Polling
}

func (i ImageModelConfig) apiConfig() apiConfig {
	return apiConfig{
		APIKey:        i.APIKey,
		BaseURL:       i.BaseURL,
		HTTPClient:    i.HTTPClient,
		OutputOrigins: i.OutputOrigins,
	}
}

func (i ImageModelConfig) Validate() error {
	if err := i.apiConfig().validate(); err != nil {
		return err
	}
	if i.DefaultOptions.Model == "" {
		return errors.New("blackforestlabs: DefaultOptions.Model is required")
	}
	if err := i.DefaultOptions.Validate(); err != nil {
		return err
	}
	if err := i.Polling.Validate(); err != nil {
		return fmt.Errorf("blackforestlabs: Polling: %w", err)
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
	polling        invocation.Polling
}

func NewImageModel(ctx context.Context, config ImageModelConfig) (*ImageModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	api, err := newAPI(ctx, config.apiConfig())
	if err != nil {
		return nil, err
	}
	return &ImageModel{api: api, defaultOptions: config.DefaultOptions.Clone(), polling: config.Polling.Or(invocation.Polling{Interval: DefaultPollInterval, Timeout: DefaultPollTimeout})}, nil
}

func (i *ImageModel) Call(ctx context.Context, req *image.Request) (*image.Response, error) {
	if err := req.Validate(); err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}
	model, apiReq, err := i.buildRequest(req)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}
	async, final, err := i.runGeneration(ctx, model, apiReq)
	if err != nil {
		return nil, err
	}
	value, err := i.downloadGeneratedImage(ctx, final.Result.Sample, apiReq.OutputFormat)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureUnknown, Cause: err}
	}
	return mapImageResponse(value, async, final)
}

func (i *ImageModel) buildRequest(req *image.Request) (string, *generateRequest, error) {
	effectiveOptions, err := i.defaultOptions.Resolve(req.Options)
	if err != nil {
		return "", nil, err
	}
	if effectiveOptions.Model == "." || effectiveOptions.Model == ".." || strings.ContainsAny(effectiveOptions.Model, "/\\?#%") {
		return "", nil, errors.New("blackforestlabs: model must be a single unescaped path segment")
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
	for _, field := range []string{requestFieldPrompt, requestFieldWidth, requestFieldHeight, requestFieldSeed, requestFieldOutputFormat} {
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
		return nil, nil, &invocation.Failure{Kind: invocation.FailureUnknown, Cause: err}
	}
	if async.ID == "" {
		return nil, nil, fmt.Errorf("%w: blackforestlabs: generation response has no task id", image.ErrInvalidResponse)
	}
	if async.PollingURL == "" {
		return nil, nil, errors.New("blackforestlabs: generation response has no polling_url")
	}
	final, err := i.pollUntilDone(ctx, async)
	if err != nil {
		return nil, nil, &invocation.Failure{Kind: invocation.FailureUnknown, Cause: err}
	}
	if final.Status != pollStatusReady {
		return nil, nil, &invocation.Failure{Kind: invocation.FailureFailed, Cause: fmt.Errorf("blackforestlabs: generation failed: %s", final.Status)}
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
	if setErr := outputMetadata.Set(outputURLMetadataKey, final.Result.Sample); setErr != nil {
		return nil, setErr
	}
	if final.Result.Seed != nil {
		if setErr := outputMetadata.Set(seedMetadataKey, *final.Result.Seed); setErr != nil {
			return nil, setErr
		}
	}
	if final.Result.Duration != nil {
		if setErr := outputMetadata.Set(durationMetadataKey, *final.Result.Duration); setErr != nil {
			return nil, setErr
		}
	}

	output, err := image.NewOutput(value, outputMetadata)
	if err != nil {
		return nil, err
	}

	meta := &image.ResponseMetadata{}
	if err := meta.Extra.Set(taskIDMetadataKey, async.ID); err != nil {
		return nil, err
	}
	if err := meta.Extra.Set(submitResponseMetadataKey, async); err != nil {
		return nil, err
	}
	if err := meta.Extra.Set(resultResponseMetadataKey, final); err != nil {
		return nil, err
	}
	return image.NewResponse([]*image.Output{output}, meta)
}

func (i *ImageModel) pollUntilDone(ctx context.Context, submitted *asyncResponse) (*pollResult, error) {
	return i.polling.Poll(ctx, func(ctx context.Context) (*pollResult, bool, error) {
		resp, err := i.api.getResult(ctx, submitted.PollingURL)
		if err != nil {
			return nil, false, err
		}
		if resp.ID != submitted.ID {
			return nil, false, fmt.Errorf("%w: blackforestlabs: polled task id %q differs from submitted id %q", image.ErrInvalidResponse, resp.ID, submitted.ID)
		}
		switch resp.Status {
		case pollStatusReady, pollStatusError, pollStatusFailed, pollStatusContentModerated, pollStatusRequestModerated:
			return resp, true, nil
		case pollStatusPending, pollStatusReasoning, pollStatusGenerating:
			return resp, false, nil
		case pollStatusTaskNotFound:
			return nil, false, fmt.Errorf("blackforestlabs: submitted task %q was not found", submitted.ID)
		default:
			return nil, false, fmt.Errorf("blackforestlabs: generation reports unrecognized status %q", resp.Status)
		}
	})
}
