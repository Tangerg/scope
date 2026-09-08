package openai

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
)

// ImageModelConfig binds provider access and defaults shared by every image call.
type ImageModelConfig struct {
	Provider       string
	APIKey         string
	DefaultOptions image.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (i ImageModelConfig) Validate() error {
	if err := validateProvider(i.Provider); err != nil {
		return fmt.Errorf("openai: Provider: %w", err)
	}
	if i.APIKey == "" {
		return errors.New("openai: APIKey is required")
	}
	if i.DefaultOptions.Model == "" {
		return errors.New("openai: DefaultOptions.Model is required")
	}
	if err := i.DefaultOptions.Validate(); err != nil {
		return err
	}
	return nil
}

var _ image.Model = (*ImageModel)(nil)

// ImageModel implements the OpenAI-compatible image protocol.
type ImageModel struct {
	api            *api
	provider       string
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
		provider:       config.Provider,
		defaultOptions: config.DefaultOptions.Clone(),
	}, nil
}

func (i *ImageModel) buildAPIImageRequest(req *image.Request) (*openai.ImageGenerateParams, error) {
	effectiveOptions, err := i.defaultOptions.Resolve(req.Options)
	if err != nil {
		return nil, err
	}
	if rejectUnsupportedOptionsErr := rejectUnsupportedOptions("openai: image", map[string]bool{
		"negative_prompt": effectiveOptions.NegativePrompt != "",
		"seed":            effectiveOptions.Seed != nil,
	}); rejectUnsupportedOptionsErr != nil {
		return nil, rejectUnsupportedOptionsErr
	}
	if (effectiveOptions.Width == nil) != (effectiveOptions.Height == nil) {
		return nil, errors.New("openai: image: width and height must be set together")
	}

	fields, err := decodeRequestFields(effectiveOptions.Extensions, protocolModalityRequestExtensionKey(i.provider, "image"), "model", "prompt", "output_format", "size")
	if err != nil {
		return nil, err
	}
	params := &openai.ImageGenerateParams{}
	params.SetExtraFields(fields)

	params.Model = effectiveOptions.Model
	params.Prompt = req.Prompt

	if effectiveOptions.OutputFormat != "" {
		params.OutputFormat = openai.ImageGenerateParamsOutputFormat(strings.TrimPrefix(effectiveOptions.OutputFormat, "image/"))
	}
	if effectiveOptions.Width != nil && effectiveOptions.Height != nil {
		params.Size = openai.ImageGenerateParamsSize(fmt.Sprintf("%dx%d", *effectiveOptions.Width, *effectiveOptions.Height))
	} else {
		params.Size = openai.ImageGenerateParamsSizeAuto
	}

	return params, nil
}

func (i *ImageModel) buildImageResponse(resp *openai.ImagesResponse, mimeType string) (*image.Response, error) {
	if len(resp.Data) == 0 {
		return nil, errors.New("openai: image response has no data")
	}

	outputs := make([]*image.Output, 0, len(resp.Data))
	for index, generated := range resp.Data {
		value, err := openAIImageMedia(mimeType, generated.URL, generated.B64JSON)
		if err != nil {
			return nil, fmt.Errorf("openai: image %d: %w", index, err)
		}
		var outputMetadata metadata.Map
		if generated.RevisedPrompt != "" {
			if setErr := outputMetadata.Set(i.provider+"/revised_prompt", generated.RevisedPrompt); setErr != nil {
				return nil, setErr
			}
		}
		output, err := image.NewOutput(value, outputMetadata)
		if err != nil {
			return nil, err
		}
		outputs = append(outputs, output)
	}

	responseMetadata := &image.ResponseMetadata{}
	if resp.Created > 0 {
		responseMetadata.CreatedAt = time.Unix(resp.Created, 0).UTC()
	}
	return image.NewResponse(outputs, responseMetadata)
}

func openAIImageMedia(mimeType, uri, encoded string) (*media.Media, error) {
	switch {
	case uri != "" && encoded == "":
		return media.NewURI(mimeType, uri)
	case uri == "" && encoded != "":
		data, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("decode base64 payload: %w", err)
		}
		return media.NewBytes(mimeType, data)
	default:
		return nil, errors.New("response must contain exactly one of URL or base64 payload")
	}
}

func (i *ImageModel) Call(ctx context.Context, req *image.Request) (*image.Response, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	apiReq, err := i.buildAPIImageRequest(req)
	if err != nil {
		return nil, err
	}

	apiResp, err := i.api.image(ctx, apiReq)
	if err != nil {
		return nil, err
	}

	mimeType := "image/png"
	if apiReq.OutputFormat != "" {
		mimeType = "image/" + string(apiReq.OutputFormat)
	}
	return i.buildImageResponse(apiResp, mimeType)
}
