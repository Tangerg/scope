package stability

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
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
		return errors.New("stability: APIKey is required")
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
// to an aspect ratio (lossy guess); set AspectRatio on the
// extension-threaded [GenerateRequest] when control is needed.
//
// [image.Options].Model selects Core, Ultra, or one exact SD 3.5 model;
// the adapter derives the official endpoint and request model field from it.
type ImageModel struct {
	api            *api
	defaultOptions image.Options
}

// NewImageModel rejects an invalid provider binding before the first image call.
func NewImageModel(_ context.Context, config ImageModelConfig) (*ImageModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	api, err := newAPI(apiConfig{
		APIKey:     config.APIKey,
		BaseURL:    config.BaseURL,
		HTTPClient: config.HTTPClient,
	})
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
	if effectiveOptions.OutputFormat != "" && apiReq.OutputFormat == "" {
		apiReq.OutputFormat = strings.TrimPrefix(effectiveOptions.OutputFormat, "image/")
	}

	// Force JSON mode to get FinishReason / Seed echoed back.
	apiReq.Mode = ResponseModeJSON
	if err := validateGenerateRequest(apiReq); err != nil {
		return "", nil, err
	}

	return endpoint, apiReq, nil
}

// checkFinishReason decides whether the envelope holds the requested image.
//
// A filtered generation still answers 200 with base64 bytes, and those bytes
// are the classifier's stand-in — Stability's own guidance is to treat the
// generation as failed. This is the reason the adapter forces JSON mode: the
// finish reason is only echoed back there, so ignoring it would make asking
// for it pointless and would hand the caller a blank image as a result. An
// unrecognized reason is refused too, because a value this adapter cannot
// classify is not evidence of success.
func checkFinishReason(reason string) error {
	switch reason {
	case "", finishReasonSuccess:
		return nil
	case finishReasonContentFiltered:
		return fmt.Errorf(
			"stability: generation filtered by the content moderation system (finish_reason %s)",
			reason,
		)
	default:
		return fmt.Errorf("stability: unrecognized finish_reason %q", reason)
	}
}

func (i *ImageModel) buildResponse(body []byte, hdr http.Header, outputFormat string) (*image.Response, error) {
	envelope, err := DecodeJSON(body)
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
	mimeType := "image/png"
	if outputFormat != "" {
		mimeType = "image/" + outputFormat
	}
	value, err := media.NewBytes(mimeType, data)
	if err != nil {
		return nil, err
	}

	var outputMetadata metadata.Map
	if envelope.FinishReason != "" {
		if setErr := outputMetadata.Set("stability/finish_reason", envelope.FinishReason); setErr != nil {
			return nil, setErr
		}
	}
	if setErr := outputMetadata.Set("stability/seed", envelope.Seed); setErr != nil {
		return nil, setErr
	}

	output, err := image.NewOutput(value, outputMetadata)
	if err != nil {
		return nil, err
	}

	meta := &image.ResponseMetadata{}
	if rid := hdr.Get("request-id"); rid != "" {
		if err := meta.Extra.Set("stability/request_id", rid); err != nil {
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
		return nil, err
	}
	endpoint, apiReq, err := i.buildAPIRequest(req)
	if err != nil {
		return nil, err
	}

	body, hdr, err := i.api.generate(ctx, endpoint, apiReq)
	if err != nil {
		return nil, err
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

func validateGenerateRequest(req *generateRequest) error {
	if req.AspectRatio != "" {
		switch req.AspectRatio {
		case "16:9", "1:1", "21:9", "2:3", "3:2", "4:5", "5:4", "9:16", "9:21":
		default:
			return fmt.Errorf("stability: unsupported aspect_ratio %q", req.AspectRatio)
		}
	}
	if req.OutputFormat != "" && req.OutputFormat != "jpeg" && req.OutputFormat != "png" && req.OutputFormat != "webp" {
		return fmt.Errorf("stability: output_format must be jpeg, png, or webp, got %q", req.OutputFormat)
	}
	if req.Seed != nil && (*req.Seed < 0 || *req.Seed > 4294967294) {
		return fmt.Errorf("stability: seed must be between 0 and 4294967294, got %d", *req.Seed)
	}
	if req.CFGScale != nil && (*req.CFGScale < 1 || *req.CFGScale > 10) {
		return fmt.Errorf("stability: cfg_scale must be between 1 and 10, got %g", *req.CFGScale)
	}
	return nil
}
