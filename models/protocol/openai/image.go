package openai

import (
	"cmp"
	"context"
	"encoding/base64"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/respjson"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
)

const imageMIMETypePrefix = "image/"

type ImageModelConfig struct {
	Provider       string
	APIKey         string
	DefaultOptions image.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (i ImageModelConfig) Validate() error {
	if err := requireBindingEndpoint(i.Provider, i.BaseURL); err != nil {
		return err
	}
	if err := validateProvider(i.Provider); err != nil {
		return fmt.Errorf("openai: Provider: %w", err)
	}
	if err := (apiConfig{APIKey: i.APIKey, BaseURL: i.BaseURL}).validate(); err != nil {
		return err
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

type ImageModel struct {
	api            *api
	provider       string
	defaultOptions image.Options
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

	fields, err := decodeRequestFields(effectiveOptions.Extensions, protocolModalityRequestExtensionKey(i.provider, "image"), "model", "prompt", "output_format", "size", "stream", "partial_images")
	if err != nil {
		return nil, err
	}
	params := &openai.ImageGenerateParams{}
	params.SetExtraFields(fields)

	params.Model = effectiveOptions.Model
	params.Prompt = req.Prompt

	if effectiveOptions.OutputFormat != "" {
		format := openai.ImagesResponseOutputFormat(strings.TrimPrefix(effectiveOptions.OutputFormat, imageMIMETypePrefix))
		if _, err := imageOutputMIMEType(format); err != nil {
			return nil, err
		}
		params.OutputFormat = openai.ImageGenerateParamsOutputFormat(format)
	}
	if effectiveOptions.Width != nil && effectiveOptions.Height != nil {
		params.Size = openai.ImageGenerateParamsSize(fmt.Sprintf("%dx%d", *effectiveOptions.Width, *effectiveOptions.Height))
	}

	return params, nil
}

func (i *ImageModel) buildImageResponse(resp *openai.ImagesResponse, requestedFormat openai.ImageGenerateParamsOutputFormat) (*image.Response, error) {
	if resp == nil || len(resp.Data) == 0 {
		return nil, fmt.Errorf("%w: openai: image response has no data", image.ErrInvalidResponse)
	}
	format := cmp.Or(openai.ImagesResponseOutputFormat(requestedFormat), openai.ImagesResponseOutputFormatPNG)
	if raw := resp.JSON.OutputFormat.Raw(); raw != respjson.Omitted && raw != respjson.Null {
		if err := jsonv2.Unmarshal([]byte(raw), &format); err != nil {
			return nil, fmt.Errorf("%w: openai: image output_format: %w", image.ErrInvalidResponse, err)
		}
	}
	mimeType, err := imageOutputMIMEType(format)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", image.ErrInvalidResponse, err)
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

func imageOutputMIMEType(format openai.ImagesResponseOutputFormat) (string, error) {
	switch format {
	case openai.ImagesResponseOutputFormatPNG, openai.ImagesResponseOutputFormatJPEG, openai.ImagesResponseOutputFormatWebP:
		return imageMIMETypePrefix + string(format), nil
	default:
		return "", fmt.Errorf("openai: image: unsupported output format %q", format)
	}
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
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}
	apiReq, err := i.buildAPIImageRequest(req)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}

	apiResp, err := i.api.image(ctx, apiReq)
	if err != nil {
		return nil, err
	}

	return i.buildImageResponse(apiResp, apiReq.OutputFormat)
}
