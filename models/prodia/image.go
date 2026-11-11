package prodia

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
)

type ImageModelConfig struct {
	APIKey         string
	DefaultOptions image.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (i ImageModelConfig) apiConfig() apiConfig {
	return apiConfig{APIKey: i.APIKey, BaseURL: i.BaseURL, HTTPClient: i.HTTPClient}
}

func (i ImageModelConfig) Validate() error {
	if err := i.apiConfig().validate(); err != nil {
		return err
	}
	if i.DefaultOptions.Model == "" {
		return errors.New("prodia: DefaultOptions.Model is required")
	}
	if err := i.DefaultOptions.Validate(); err != nil {
		return err
	}
	return nil
}

var _ image.Model = (*ImageModel)(nil)

// ImageModel wraps Prodia's /v2/job inference endpoint. Model id
// ([image.Options].Model) carries the full Prodia type, e.g.
// "inference.flux.dev.txt2img.v1". Core owns the prompt, model, dimensions,
// negative prompt, and seed. The native config extension accepts provider-only
// options such as sampler and steps; duplicate Core fields are rejected.
type ImageModel struct {
	api            *api
	defaultOptions image.Options
}

func NewImageModel(ctx context.Context, config ImageModelConfig) (*ImageModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	api, err := newAPI(ctx, config.apiConfig())
	if err != nil {
		return nil, err
	}
	return &ImageModel{api: api, defaultOptions: config.DefaultOptions.Clone()}, nil
}

func (i *ImageModel) Call(ctx context.Context, req *image.Request) (*image.Response, error) {
	if err := req.Validate(); err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}
	effectiveOptions, err := i.defaultOptions.Resolve(req.Options)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}
	apiReq, err := newJobRequest(req.Prompt, effectiveOptions)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}
	accept := effectiveOptions.OutputFormat
	switch accept {
	case "", mediaTypeJPEG, mediaTypePNG, mediaTypeWebP:
	default:
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: errors.New("prodia: image: output_format must be image/jpeg, image/png, or image/webp")}
	}
	body, hdr, err := i.api.job(ctx, apiReq, accept)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureUnknown, Cause: err}
	}
	return newJobResponse(apiReq.Type, body, hdr.Get(headerContentType), accept)
}

// newJobRequest merges the caller's native job extension with the Core
// prompt, model and options, rejecting extension fields Core owns.
func newJobRequest(prompt string, options image.Options) (*jobRequest, error) {
	nativeFields, _, err := options.Extensions.Decode[map[string]any](ImageRequestExtensionKey)
	if err != nil {
		return nil, err
	}
	if _, exists := nativeFields[fieldType]; exists {
		return nil, fmt.Errorf("prodia: extension %q field %q: %w", ImageRequestExtensionKey, fieldType, metadata.ErrCoreOwnedField)
	}
	request, _, err := options.Extensions.Decode[jobRequest](ImageRequestExtensionKey)
	if err != nil {
		return nil, err
	}
	request.Type = options.Model
	if !strings.Contains(request.Type, textToImageJobSegment) {
		return nil, errors.New("prodia: image model requires a text-to-image job type containing .txt2img")
	}
	for _, field := range []string{fieldPrompt, fieldNegativePrompt, fieldWidth, fieldHeight, fieldSeed} {
		if _, exists := request.Config[field]; exists {
			return nil, fmt.Errorf("prodia: config field %q: %w", field, metadata.ErrCoreOwnedField)
		}
	}
	if err := request.setConfig(fieldPrompt, prompt); err != nil {
		return nil, err
	}
	if err := request.setCoreOptions(options); err != nil {
		return nil, err
	}
	return &request, nil
}

func newJobResponse(jobType string, body []byte, contentType, accept string) (*image.Response, error) {
	value, err := media.NewBytes(cmp.Or(contentType, accept, mediaTypeBinary), body)
	if err != nil {
		return nil, err
	}
	output, err := image.NewOutput(value, nil)
	if err != nil {
		return nil, err
	}
	metadata := &image.ResponseMetadata{}
	if err := metadata.Extra.Set(metadataJobType, jobType); err != nil {
		return nil, err
	}
	return image.NewResponse([]*image.Output{output}, metadata)
}
