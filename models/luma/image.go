package luma

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
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
	OutputOrigins []string
	// Polling bounds the wait for the async Luma generation; zero fields
	// select DefaultPollInterval and DefaultPollTimeout.
	Polling        invocation.Polling
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
	if err := i.Polling.Validate(); err != nil {
		return fmt.Errorf("luma: Polling: %w", err)
	}
	if i.MaxOutputBytes < 0 {
		return errors.New("luma: MaxOutputBytes must not be negative")
	}
	_, err := outputOrigins(i.OutputOrigins)
	return err
}

var _ image.Model = (*ImageModel)(nil)

// ImageModel implements Luma Agents image generation and editing with the
// official SDK. Provider output URLs are downloaded before they expire.
type ImageModel struct {
	api            *api
	defaultOptions image.Options
	httpClient     *http.Client
	polling        invocation.Polling
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
	origins, err := outputOrigins(config.OutputOrigins)
	if err != nil {
		return nil, err
	}
	// Outputs never receive credentials, even when they share the API host.
	downloads, err := httpclient.NewDeliveryClient(httpclient.DeliveryConfig{HTTPClient: config.HTTPClient, Destination: origins})
	if err != nil {
		return nil, err
	}
	return &ImageModel{
		api:            api,
		defaultOptions: config.DefaultOptions.Clone(),
		httpClient:     downloads,
		polling:        config.Polling.Or(invocation.Polling{Interval: DefaultPollInterval, Timeout: DefaultPollTimeout}),
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
	params, err := i.generationParams(request)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
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

// generationParams resolves a validated request into the native generation.
func (i *ImageModel) generationParams(request *image.Request) (lumaagents.GenerationNewParams, error) {
	effectiveOptions, err := i.defaultOptions.Resolve(request.Options)
	if err != nil {
		return lumaagents.GenerationNewParams{}, err
	}
	if err = rejectUnsupportedOptions(effectiveOptions); err != nil {
		return lumaagents.GenerationNewParams{}, err
	}
	// Decoding the extension validates it.
	options, _, err := effectiveOptions.Extensions.Decode[ImageRequestOptions](ImageRequestExtensionKey)
	if err != nil {
		return lumaagents.GenerationNewParams{}, fmt.Errorf("luma: extension %q: %w", ImageRequestExtensionKey, err)
	}
	params := lumaagents.GenerationNewParams{
		Prompt: lumaagents.F(request.Prompt),
		Model:  lumaagents.F(lumaagents.Model(effectiveOptions.Model)),
		Type:   lumaagents.F(lumaagents.GenerationNewParamsTypeImage),
	}
	options.apply(&params)
	if effectiveOptions.OutputFormat != "" {
		format, err := nativeOutputFormat(effectiveOptions.OutputFormat)
		if err != nil {
			return lumaagents.GenerationNewParams{}, err
		}
		params.OutputFormat = lumaagents.F(format)
	}
	return params, nil
}

func nativeOutputFormat(mediaType media.Type) (lumaagents.GenerationNewParamsOutputFormat, error) {
	switch mediaType.Subtype() {
	case string(lumaagents.GenerationNewParamsOutputFormatPng):
		return lumaagents.GenerationNewParamsOutputFormatPng, nil
	case string(lumaagents.GenerationNewParamsOutputFormatJpeg), outputFormatJPG:
		return lumaagents.GenerationNewParamsOutputFormatJpeg, nil
	default:
		return "", fmt.Errorf("luma: output format %q is unsupported; use image/png or image/jpeg", mediaType)
	}
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
	return i.polling.Poll(ctx, func(ctx context.Context) (*lumaagents.Generation, bool, error) {
		generation, err := i.api.getGeneration(ctx, generationID)
		if err != nil {
			return nil, false, fmt.Errorf("luma: get generation %q: %w", generationID, err)
		}
		switch generation.State {
		case lumaagents.GenerationStateCompleted, lumaagents.GenerationStateFailed:
			return generation, true, nil
		case lumaagents.GenerationStateQueued, lumaagents.GenerationStateProcessing:
			return generation, false, nil
		default:
			return nil, false, fmt.Errorf("luma: generation %q reports unrecognized state %q", generationID, generation.State)
		}
	})
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
		mediaType, parseErr := media.ParseType(contentType)
		if parseErr != nil {
			return nil, "", fmt.Errorf("download content type: %w", parseErr)
		}
		mimeType = string(mediaType)
	} else {
		mimeType = http.DetectContentType(data)
	}
	return data, mimeType, nil
}
