package stability

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"

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
		return errors.New("stability: DefaultOptions.Model is required")
	}
	if err := i.DefaultOptions.Validate(); err != nil {
		return err
	}
	return nil
}

var _ image.Model = (*ImageModel)(nil)

// ImageModel wraps Stability AI's Stable Image / SD3 endpoints.
//
// Stability uses an aspect-ratio code ("1:1" / "16:9" / ...) rather
// than per-pixel W×H sizes — Core/Ultra render at a fixed total pixel
// budget. Scope's Width/Height options are intentionally NOT translated
// to an aspect ratio; set aspect_ratio under RequestExtensionKey when
// control is needed.
//
// [image.Options].Model selects Core, Ultra, or one exact SD 3.5 model;
// the adapter derives the official endpoint and request model field from it.
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

	return &ImageModel{
		api:            api,
		defaultOptions: config.DefaultOptions.Clone(),
	}, nil
}

func (i *ImageModel) buildAPIRequest(req *image.Request) (string, *generateRequest, error) {
	effectiveOptions, err := i.defaultOptions.Resolve(req.Options)
	if err != nil {
		return "", nil, err
	}
	switch {
	case effectiveOptions.Height != nil && effectiveOptions.Width != nil:
		return "", nil, errors.New("stability: image: unsupported options: height, width")
	case effectiveOptions.Height != nil:
		return "", nil, errors.New("stability: image: unsupported option: height")
	case effectiveOptions.Width != nil:
		return "", nil, errors.New("stability: image: unsupported option: width")
	}

	nativeFields, _, decodeErr := effectiveOptions.Extensions.Decode[map[string]any](RequestExtensionKey)
	if decodeErr != nil {
		return "", nil, decodeErr
	}
	for _, field := range []string{fieldPrompt, fieldNegativePrompt, fieldSeed, fieldOutputFormat, fieldModel} {
		if _, exists := nativeFields[field]; exists {
			return "", nil, fmt.Errorf("stability: extension %q field %q: %w", RequestExtensionKey, field, metadata.ErrCoreOwnedField)
		}
	}
	if _, exists := nativeFields[fieldMode]; exists {
		return "", nil, errors.New("stability: extension mode is managed by the image adapter")
	}

	apiReqValue, _, err := effectiveOptions.Extensions.Decode[generateRequest](RequestExtensionKey)

	apiReq := &apiReqValue
	if err != nil {
		return "", nil, err
	}
	endpoint, wireModel, err := resolveModel(effectiveOptions.Model)
	if err != nil {
		return "", nil, err
	}
	apiReq.Model = wireModel

	apiReq.Prompt = req.Prompt
	if effectiveOptions.NegativePrompt != "" {
		apiReq.NegativePrompt = effectiveOptions.NegativePrompt
	}
	if effectiveOptions.Seed != nil {
		apiReq.Seed = effectiveOptions.Seed
	}
	if effectiveOptions.OutputFormat != "" {
		apiReq.OutputFormat = effectiveOptions.OutputFormat.Subtype()
	}

	// Binary responses omit the verdict needed to reject filtered generations.
	apiReq.Mode = ResponseModeJSON
	if err := apiReq.validate(endpoint); err != nil {
		return "", nil, err
	}

	return endpoint, apiReq, nil
}

// Filtered bytes are a stand-in, not evidence of a successful generation.
func checkFinishReason(reason string) error {
	switch reason {
	case finishReasonSuccess:
		return nil
	case finishReasonContentFiltered:
		return &invocation.Failure{
			Kind:  invocation.FailureFailed,
			Cause: fmt.Errorf("stability: generation filtered by the content moderation system (finish_reason %s)", reason),
		}
	default:
		return fmt.Errorf("stability: unrecognized finish_reason %q", reason)
	}
}

func (i *ImageModel) buildResponse(body []byte, hdr http.Header, outputFormat string) (*image.Response, error) {
	envelope, err := decodeJSON(body)
	if err != nil {
		return nil, err
	}
	if reasonErr := checkFinishReason(envelope.FinishReason); reasonErr != nil {
		return nil, reasonErr
	}

	data, err := base64.StdEncoding.DecodeString(envelope.Image)
	if err != nil {
		return nil, fmt.Errorf("stability: decode image: %w", err)
	}
	mimeType := mediaTypePNG
	if outputFormat != "" {
		mimeType = string(media.TopLevelImage.Type(outputFormat))
	}
	value, err := media.NewBytes(mimeType, data)
	if err != nil {
		return nil, err
	}

	var outputMetadata metadata.Map
	if envelope.FinishReason != "" {
		if setErr := outputMetadata.Set(metadataFinishReason, envelope.FinishReason); setErr != nil {
			return nil, setErr
		}
	}
	if envelope.Seed != nil {
		if *envelope.Seed < 0 || *envelope.Seed > maximumSeed {
			return nil, fmt.Errorf("stability: invalid response seed %d", *envelope.Seed)
		}
		if setErr := outputMetadata.Set(metadataSeed, *envelope.Seed); setErr != nil {
			return nil, setErr
		}
	}

	output, err := image.NewOutput(value, outputMetadata)
	if err != nil {
		return nil, err
	}

	meta := &image.ResponseMetadata{}
	if rid := hdr.Get(headerRequestID); rid != "" {
		if err := meta.Extra.Set(metadataRequestID, rid); err != nil {
			return nil, err
		}
	}
	if err := meta.Extra.Set(ResponseExtensionKey, envelope); err != nil {
		return nil, err
	}

	return image.NewResponse([]*image.Output{output}, meta)
}

func (i *ImageModel) Call(ctx context.Context, req *image.Request) (*image.Response, error) {
	if err := req.Validate(); err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}
	endpoint, apiReq, err := i.buildAPIRequest(req)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}

	body, hdr, err := i.api.generate(ctx, endpoint, apiReq)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureUnknown, Cause: err}
	}

	return i.buildResponse(body, hdr, apiReq.OutputFormat)
}

func resolveModel(model string) (endpoint, wireModel string, err error) {
	switch model {
	case ModelCore:
		return endpointCore, "", nil
	case ModelUltra:
		return endpointUltra, "", nil
	case ModelSD3Point5Large, ModelSD3Point5LargeTurbo, ModelSD3Point5Medium, ModelSD3Point5Flash:
		return endpointSD3, model, nil
	default:
		return "", "", fmt.Errorf("stability: unsupported image model %q", model)
	}
}
