package luma

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"slices"
	"strings"
	"time"

	lumaagents "github.com/lumalabs/luma-agents-go"

	"github.com/Tangerg/scope/core/httpclient"
	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
)

// DefaultMaxOutputBytes bounds how much generated media is read into memory. A
// provider that returns an unexpectedly large asset would otherwise be able to
// exhaust the process before the response is ever validated.
const DefaultMaxOutputBytes = int64(32 * 1024 * 1024)

type ImageModelConfig struct {
	APIKey         string
	DefaultOptions image.Options
	BaseURL        string
	HTTPClient     *http.Client
	// OutputOrigins is the host-authorized set of origins for presigned assets.
	// The output client never borrows API credentials or the HTTP client cookie jar.
	OutputOrigins  []string
	PollInterval   time.Duration
	PollTimeout    time.Duration
	MaxOutputBytes int64
}

func (i ImageModelConfig) apiConfig() apiConfig {
	return apiConfig{
		APIKey:     i.APIKey,
		BaseURL:    i.BaseURL,
		HTTPClient: i.HTTPClient,
	}
}

func (i ImageModelConfig) Validate() error {
	if err := i.apiConfig().validate(); err != nil {
		return err
	}
	if i.DefaultOptions.Model == "" {
		return errors.New("luma: DefaultOptions.Model is required")
	}
	if err := i.DefaultOptions.Validate(); err != nil {
		return fmt.Errorf("luma: DefaultOptions: %w", err)
	}
	if i.PollInterval < 0 {
		return errors.New("luma: PollInterval must not be negative")
	}
	if i.PollTimeout < 0 {
		return errors.New("luma: PollTimeout must not be negative")
	}
	if i.MaxOutputBytes < 0 {
		return errors.New("luma: MaxOutputBytes must not be negative")
	}
	_, err := newOutputPolicy(context.Background(), i.OutputOrigins)
	return err
}

var _ image.Model = (*ImageModel)(nil)

// ImageModel implements Luma Agents image generation and editing with the
// official SDK. Provider output URLs are downloaded before they expire.
type ImageModel struct {
	api            *api
	defaultOptions image.Options
	httpClient     *http.Client
	pollInterval   time.Duration
	pollTimeout    time.Duration
	maxOutputBytes int64
}

func NewImageModel(ctx context.Context, config ImageModelConfig) (*ImageModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	api, err := newAPI(ctx, config.apiConfig())
	if err != nil {
		return nil, err
	}
	outputs, err := newOutputPolicy(ctx, config.OutputOrigins)
	if err != nil {
		return nil, err
	}
	pollInterval := config.PollInterval
	if pollInterval == 0 {
		pollInterval = DefaultPollInterval
	}
	pollTimeout := config.PollTimeout
	if pollTimeout == 0 {
		pollTimeout = DefaultPollTimeout
	}
	return &ImageModel{
		api:            api,
		defaultOptions: config.DefaultOptions.Clone(),
		httpClient:     outputs.client(config.HTTPClient),
		pollInterval:   pollInterval,
		pollTimeout:    pollTimeout,
		maxOutputBytes: cmp.Or(config.MaxOutputBytes, DefaultMaxOutputBytes),
	}, nil
}

func (i *ImageModel) Call(ctx context.Context, request *image.Request) (*image.Response, error) {
	if i == nil || i.api == nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: errors.New("luma: nil ImageModel")}
	}
	if err := request.Validate(); err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: fmt.Errorf("luma: request: %w", err)}
	}
	effectiveOptions, err := i.defaultOptions.Resolve(request.Options)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}
	if rejectUnsupportedOptionsErr := rejectUnsupportedOptions(effectiveOptions); rejectUnsupportedOptionsErr != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: rejectUnsupportedOptionsErr}
	}

	options, _, err := effectiveOptions.Extensions.Decode[ImageRequestOptions](ImageRequestExtensionKey)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: fmt.Errorf("luma: extension %q: %w", ImageRequestExtensionKey, err)}
	}
	if err = options.Validate(); err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}
	params := lumaagents.GenerationNewParams{
		Prompt: lumaagents.F(request.Prompt),
		Model:  lumaagents.F(lumaagents.Model(effectiveOptions.Model)),
		Type:   lumaagents.F(lumaagents.GenerationNewParamsTypeImage),
	}
	if options.Type != "" {
		params.Type = lumaagents.F(lumaagents.GenerationNewParamsType(options.Type))
	}
	if options.AspectRatio != "" {
		params.AspectRatio = lumaagents.F(lumaagents.GenerationNewParamsAspectRatio(options.AspectRatio))
	}
	if options.Source != nil {
		params.Source = lumaagents.F(options.Source.sdkParams())
	}
	if options.ImageRef != nil {
		references := make([]lumaagents.ImageRefParam, len(options.ImageRef))
		for index, reference := range options.ImageRef {
			references[index] = reference.sdkParams()
		}
		params.ImageRef = lumaagents.F(references)
	}
	if options.Style != "" {
		params.Style = lumaagents.F(lumaagents.GenerationNewParamsStyle(options.Style))
	}
	if options.UserID != "" {
		params.UserID = lumaagents.F(options.UserID)
	}
	if options.WebSearch != nil {
		params.WebSearch = lumaagents.F(*options.WebSearch)
	}
	if effectiveOptions.OutputFormat != "" {
		format := strings.TrimPrefix(effectiveOptions.OutputFormat, mediaTypeImagePrefix)
		switch format {
		case string(lumaagents.GenerationNewParamsOutputFormatPng):
			params.OutputFormat = lumaagents.F(lumaagents.GenerationNewParamsOutputFormatPng)
		case string(lumaagents.GenerationNewParamsOutputFormatJpeg), outputFormatJPG:
			params.OutputFormat = lumaagents.F(lumaagents.GenerationNewParamsOutputFormatJpeg)
		default:
			return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: fmt.Errorf("luma: output format %q is unsupported; use image/png or image/jpeg", effectiveOptions.OutputFormat)}
		}
	}

	submitted, err := i.api.createGeneration(ctx, params)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureUnknown, Cause: fmt.Errorf("luma: create generation: %w", err)}
	}
	completed, err := i.pollUntilDone(ctx, submitted.ID)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureUnknown, Cause: err}
	}
	if completed.State == lumaagents.GenerationStateFailed {
		return nil, &invocation.Failure{Kind: invocation.FailureFailed, Cause: fmt.Errorf("luma: generation %q failed (%s): %s", completed.ID, completed.FailureCode, completed.FailureReason)}
	}
	response, err := i.mapResponse(ctx, completed)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureUnknown, Cause: err}
	}
	return response, nil
}

func rejectUnsupportedOptions(options image.Options) error {
	var unsupported []string
	if options.Height != nil {
		unsupported = append(unsupported, "height")
	}
	if options.NegativePrompt != "" {
		unsupported = append(unsupported, "negative_prompt")
	}
	if options.Seed != nil {
		unsupported = append(unsupported, "seed")
	}
	if options.Width != nil {
		unsupported = append(unsupported, "width")
	}
	if len(unsupported) == 0 {
		return nil
	}
	slices.Sort(unsupported)
	return fmt.Errorf("luma: image: unsupported options: %s", strings.Join(unsupported, ", "))
}

func (i *ImageModel) pollUntilDone(ctx context.Context, generationID string) (*lumaagents.Generation, error) {
	deadline, cancel := context.WithTimeout(ctx, i.pollTimeout)
	defer cancel()
	ticker := time.NewTicker(i.pollInterval)
	defer ticker.Stop()
	for {
		generation, err := i.api.getGeneration(deadline, generationID)
		if err != nil {
			return nil, fmt.Errorf("luma: get generation %q: %w", generationID, err)
		}
		switch generation.State {
		case lumaagents.GenerationStateCompleted, lumaagents.GenerationStateFailed:
			return generation, nil
		case lumaagents.GenerationStateQueued, lumaagents.GenerationStateProcessing:
		default:
			return nil, fmt.Errorf("luma: generation %q reports unrecognized state %q", generationID, generation.State)
		}
		select {
		case <-deadline.Done():
			return nil, deadline.Err()
		case <-ticker.C:
		}
	}
}

func (i *ImageModel) mapResponse(ctx context.Context, generation *lumaagents.Generation) (*image.Response, error) {
	if generation == nil {
		return nil, errors.New("luma: nil generation response")
	}
	if len(generation.Output) == 0 {
		return nil, fmt.Errorf("luma: generation %q completed without output", generation.ID)
	}
	outputs := make([]*image.Output, 0, len(generation.Output))
	for outputIndex := range generation.Output {
		generationOutput := generation.Output[outputIndex]
		data, mimeType, err := i.downloadOutput(ctx, generationOutput.URL)
		if err != nil {
			return nil, fmt.Errorf("luma: output[%d]: %w", outputIndex, err)
		}
		value, err := media.NewBytes(mimeType, data)
		if err != nil {
			return nil, err
		}
		var outputMetadata metadata.Map
		if setErr := outputMetadata.Set(metadataOutput, generationOutput); setErr != nil {
			return nil, setErr
		}
		output, err := image.NewOutput(value, outputMetadata)
		if err != nil {
			return nil, err
		}
		outputs = append(outputs, output)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, generation.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("luma: generation created_at %q: %w", generation.CreatedAt, err)
	}
	metadata := &image.ResponseMetadata{CreatedAt: createdAt.UTC()}
	if err := metadata.Extra.Set(ResponseExtensionKey, generation); err != nil {
		return nil, err
	}
	return image.NewResponse(outputs, metadata)
}

func (i *ImageModel) downloadOutput(ctx context.Context, rawURL string) ([]byte, string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("build download request: %w", err)
	}
	response, err := i.httpClient.Do(request)
	if err != nil {
		return nil, "", fmt.Errorf("download: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, "", fmt.Errorf("download: %w", httpclient.NewResponseError(response.StatusCode, response.Header))
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, i.maxOutputBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("read download: %w", err)
	}
	if int64(len(data)) > i.maxOutputBytes {
		return nil, "", fmt.Errorf("download exceeds %d-byte limit", i.maxOutputBytes)
	}
	if len(data) == 0 {
		return nil, "", errors.New("download is empty")
	}
	mimeType := ""
	contentType := strings.TrimSpace(response.Header.Get(headerContentType))
	if contentType != "" {
		mimeType, _, err = mime.ParseMediaType(contentType)
		if err != nil {
			return nil, "", fmt.Errorf("parse download content type %q: %w", contentType, err)
		}
	} else {
		mimeType = http.DetectContentType(data)
	}
	return data, mimeType, nil
}
