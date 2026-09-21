package prodia

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/media"
)

// ImageModelConfig binds provider access and defaults shared by every image call.
type ImageModelConfig struct {
	APIKey         string
	DefaultOptions image.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (i ImageModelConfig) Validate() error {
	if i.APIKey == "" {
		return errors.New("prodia: APIKey is required")
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

// NewImageModel rejects an invalid provider binding before the first image call.
func NewImageModel(_ context.Context, config ImageModelConfig) (*ImageModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	api, err := newAPI(apiConfig{APIKey: config.APIKey, BaseURL: config.BaseURL, HTTPClient: config.HTTPClient})
	if err != nil {
		return nil, err
	}
	return &ImageModel{api: api, defaultOptions: config.DefaultOptions.Clone()}, nil
}

func (i *ImageModel) Call(ctx context.Context, req *image.Request) (*image.Response, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	effectiveOptions, err := i.defaultOptions.Resolve(req.Options)
	if err != nil {
		return nil, err
	}
	nativeFields, _, decodeErr := effectiveOptions.Extensions.Decode[map[string]any](ImageRequestExtensionKey)
	if decodeErr != nil {
		return nil, decodeErr
	}
	for _, field := range []string{"type"} {
		if _, exists := nativeFields[field]; exists {
			return nil, fmt.Errorf("prodia: extension %q field %q is owned by Core", ImageRequestExtensionKey, field)
		}
	}

	apiReqValue, _, err := effectiveOptions.Extensions.Decode[jobRequest](ImageRequestExtensionKey)
	apiReq := &apiReqValue
	if err != nil {
		return nil, err
	}
	apiReq.Type = effectiveOptions.Model
	if !strings.Contains(apiReq.Type, ".txt2img.") {
		return nil, errors.New("prodia: image model requires a text-to-image job type containing .txt2img")
	}
	if apiReq.Config == nil {
		apiReq.Config = map[string]any{}
	}
	for _, field := range []string{"prompt", "negative_prompt", "width", "height", "seed"} {
		if _, exists := apiReq.Config[field]; exists {
			return nil, fmt.Errorf("prodia: config field %q is owned by Core", field)
		}
	}
	apiReq.Config["prompt"] = req.Prompt
	if effectiveOptions.NegativePrompt != "" {
		apiReq.Config["negative_prompt"] = effectiveOptions.NegativePrompt
	}
	if effectiveOptions.Width != nil {
		apiReq.Config["width"] = *effectiveOptions.Width
	}
	if effectiveOptions.Height != nil {
		apiReq.Config["height"] = *effectiveOptions.Height
	}
	if effectiveOptions.Seed != nil {
		apiReq.Config["seed"] = *effectiveOptions.Seed
	}

	accept := effectiveOptions.OutputFormat
	switch accept {
	case "", "image/jpeg", "image/png", "image/webp":
	default:
		return nil, errors.New("prodia: image: output_format must be image/jpeg, image/png, or image/webp")
	}
	body, hdr, err := i.api.job(ctx, apiReq, accept)
	if err != nil {
		return nil, err
	}

	mimeType := hdr.Get("Content-Type")
	if mimeType == "" {
		mimeType = accept
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	value, err := media.NewBytes(mimeType, body)
	if err != nil {
		return nil, err
	}

	output, err := image.NewOutput(value, nil)
	if err != nil {
		return nil, err
	}

	metadata := &image.ResponseMetadata{}
	if err := metadata.Extra.Set("prodia/job_type", apiReq.Type); err != nil {
		return nil, err
	}
	return image.NewResponse([]*image.Output{output}, metadata)
}
