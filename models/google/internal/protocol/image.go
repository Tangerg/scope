package protocol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"mime"
	"slices"
	"strings"
	"time"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
)

const (
	imageInteractionType = "image"
	imageMediaTypePrefix = "image/"
	mediaTypePNG         = "image/png"
	mediaTypeJPEG        = "image/jpeg"
)

// ImageModelConfig binds provider access and defaults shared by every image call.
type ImageModelConfig struct {
	Client         ClientConfig
	DefaultOptions image.Options
}

func (i ImageModelConfig) Validate() error {
	if err := i.Client.Validate(); err != nil {
		return err
	}
	if i.DefaultOptions.Model == "" {
		return errors.New("google: DefaultOptions.Model is required")
	}
	if err := i.DefaultOptions.Validate(); err != nil {
		return err
	}
	return nil
}

// ImageGenerationOptions carries the current Interactions API controls that
// do not have provider-neutral equivalents in [image.Options]. Store it under
// [ImageRequestExtensionKey].
type ImageGenerationOptions struct {
	AspectRatio           string                    `json:"aspect_ratio,omitempty"`
	ImageSize             string                    `json:"image_size,omitempty"`
	Delivery              string                    `json:"delivery,omitempty"`
	PreviousInteractionID string                    `json:"previous_interaction_id,omitempty"`
	Store                 *bool                     `json:"store,omitempty"`
	ThinkingLevel         string                    `json:"thinking_level,omitempty"`
	ThinkingSummaries     string                    `json:"thinking_summaries,omitempty"`
	ServiceTier           string                    `json:"service_tier,omitempty"`
	Labels                map[string]string         `json:"labels,omitempty"`
	InputImages           []*media.Media            `json:"input_images,omitempty"`
	GoogleSearch          *ImageGoogleSearchOptions `json:"google_search,omitempty"`
	SafetySettings        []ImageSafetySetting      `json:"safety_settings,omitempty"`
}

func (i *ImageGenerationOptions) validate(modelName string) error {
	if i == nil {
		return errors.New("google: image: nil provider options")
	}
	if err := i.validateValues(); err != nil {
		return err
	}
	if err := i.validateModel(modelName); err != nil {
		return err
	}
	return validateImageSafetySettings(i.SafetySettings)
}

func (i *ImageGenerationOptions) validateValues() error {
	if i.AspectRatio != "" && !slices.Contains([]string{
		"1:1", "2:3", "3:2", "3:4", "4:3", "4:5", "5:4", "9:16", "16:9", "21:9", "1:8", "8:1", "1:4", "4:1",
	}, i.AspectRatio) {
		return fmt.Errorf("google: image: unsupported aspect ratio %q", i.AspectRatio)
	}
	if i.ImageSize != "" && !slices.Contains([]string{"512", "1K", "2K", "4K"}, i.ImageSize) {
		return fmt.Errorf("google: image: unsupported image size %q", i.ImageSize)
	}
	if i.Delivery != "" && i.Delivery != "inline" && i.Delivery != "uri" {
		return fmt.Errorf("google: image: unsupported delivery %q", i.Delivery)
	}
	if i.ThinkingLevel != "" && !slices.Contains([]string{"minimal", "low", "medium", "high"}, i.ThinkingLevel) {
		return fmt.Errorf("google: image: unsupported thinking level %q", i.ThinkingLevel)
	}
	if i.ThinkingSummaries != "" && i.ThinkingSummaries != "auto" && i.ThinkingSummaries != "none" {
		return fmt.Errorf("google: image: unsupported thinking summaries %q", i.ThinkingSummaries)
	}
	if i.ServiceTier != "" && !slices.Contains([]string{"flex", "standard", "priority"}, i.ServiceTier) {
		return fmt.Errorf("google: image: unsupported service tier %q", i.ServiceTier)
	}
	if i.GoogleSearch != nil {
		for index, searchType := range i.GoogleSearch.SearchTypes {
			if searchType != "web_search" && searchType != "image_search" {
				return fmt.Errorf("google: image: google_search.search_types[%d]: unsupported value %q", index, searchType)
			}
		}
	}
	return nil
}

func (i *ImageGenerationOptions) validateModel(modelName string) error {
	switch modelName {
	case ModelGemini31FlashLiteImage:
		if i.ImageSize != "" && i.ImageSize != "1K" {
			return fmt.Errorf("google: image: model %q only supports image size 1K", modelName)
		}
		if i.GoogleSearch != nil {
			return fmt.Errorf("google: image: model %q does not support Google Search grounding", modelName)
		}
		if i.ThinkingLevel != "" && i.ThinkingLevel != "minimal" && i.ThinkingLevel != "high" {
			return fmt.Errorf("google: image: model %q only supports minimal or high thinking", modelName)
		}
	case ModelGemini25FlashImage:
		if i.ImageSize != "" && i.ImageSize != "1K" {
			return fmt.Errorf("google: image: model %q only supports image size 1K", modelName)
		}
	case ModelGemini3ProImage:
		if i.ImageSize == "512" {
			return fmt.Errorf("google: image: model %q does not support image size 512", modelName)
		}
	}
	return nil
}

// ImageGoogleSearchOptions configures the image-generation guide's
// google_search tool. SearchTypes accepts "web_search" and "image_search".
type ImageGoogleSearchOptions struct {
	SearchTypes []string `json:"search_types,omitempty"`
}

// ImageSafetySetting mirrors the Interactions API safety-setting shape.
type ImageSafetySetting struct {
	Type      string `json:"type"`
	Threshold string `json:"threshold"`
	Method    string `json:"method,omitempty"`
}

var _ image.Model = (*ImageModel)(nil)

// ImageModel uses the current Gemini Interactions API. Imagen's legacy
// GenerateImages endpoint is deliberately not exposed: Google has deprecated
// Imagen and scheduled it for shutdown on 2026-08-17.
type ImageModel struct {
	api            *api
	defaultOptions image.Options
}

// NewImageModel rejects an invalid provider binding before the first image call.
func NewImageModel(ctx context.Context, config ImageModelConfig) (*ImageModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	api, err := newAPI(ctx, config.Client)
	if err != nil {
		return nil, err
	}

	return &ImageModel{
		api:            api,
		defaultOptions: config.DefaultOptions.Clone(),
	}, nil
}

type imageInteractionRequest struct {
	Model                 string                            `json:"model"`
	Input                 any                               `json:"input"`
	Tools                 []imageInteractionTool            `json:"tools,omitempty"`
	ResponseFormat        imageInteractionResponseFormat    `json:"response_format"`
	Store                 *bool                             `json:"store,omitempty"`
	GenerationConfig      *imageInteractionGenerationConfig `json:"generation_config,omitempty"`
	PreviousInteractionID string                            `json:"previous_interaction_id,omitempty"`
	Labels                map[string]string                 `json:"labels,omitempty"`
	SafetySettings        []ImageSafetySetting              `json:"safety_settings,omitempty"`
	ServiceTier           string                            `json:"service_tier,omitempty"`
}

type imageInteractionContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     []byte `json:"data,omitempty"`
	URI      string `json:"uri,omitempty"`
	MIMEType string `json:"mime_type,omitempty"`
}

type imageInteractionTool struct {
	Type        string   `json:"type"`
	SearchTypes []string `json:"search_types,omitempty"`
}

type imageInteractionResponseFormat struct {
	Type        string `json:"type"`
	MIMEType    string `json:"mime_type,omitempty"`
	AspectRatio string `json:"aspect_ratio,omitempty"`
	ImageSize   string `json:"image_size,omitempty"`
	Delivery    string `json:"delivery,omitempty"`
}

type imageInteractionGenerationConfig struct {
	Seed              *int32 `json:"seed,omitempty"`
	ThinkingLevel     string `json:"thinking_level,omitempty"`
	ThinkingSummaries string `json:"thinking_summaries,omitempty"`
}

func (i *ImageModel) buildAPIRequest(req *image.Request) (*imageInteractionRequest, error) {
	effectiveOptions, err := i.defaultOptions.Resolve(req.Options)
	if err != nil {
		return nil, err
	}
	if validateOptionsErr := i.validateOptions(effectiveOptions); validateOptionsErr != nil {
		return nil, validateOptionsErr
	}

	providerOptsValue, _, err := effectiveOptions.Extensions.Decode[ImageGenerationOptions](ImageRequestExtensionKey)

	providerOpts := &providerOptsValue
	if err != nil {
		return nil, err
	}
	if err := providerOpts.validate(effectiveOptions.Model); err != nil {
		return nil, err
	}
	if effectiveOptions.OutputFormat != "" && effectiveOptions.OutputFormat != mediaTypePNG && effectiveOptions.OutputFormat != mediaTypeJPEG {
		return nil, fmt.Errorf("google: image: unsupported output format %q; use %s or %s",
			effectiveOptions.OutputFormat, mediaTypePNG, mediaTypeJPEG)
	}

	responseFormat := imageInteractionResponseFormat{
		Type:        imageInteractionType,
		MIMEType:    effectiveOptions.OutputFormat,
		AspectRatio: providerOpts.AspectRatio,
		ImageSize:   providerOpts.ImageSize,
		Delivery:    providerOpts.Delivery,
	}
	generationConfig := &imageInteractionGenerationConfig{
		ThinkingLevel:     providerOpts.ThinkingLevel,
		ThinkingSummaries: providerOpts.ThinkingSummaries,
	}
	if effectiveOptions.Seed != nil {
		if *effectiveOptions.Seed > int64(math.MaxInt32) {
			return nil, fmt.Errorf("google: image: seed exceeds int32: %d", *effectiveOptions.Seed)
		}
		generationConfig.Seed = new(int32(*effectiveOptions.Seed))
	}
	if generationConfig.Seed == nil && generationConfig.ThinkingLevel == "" && generationConfig.ThinkingSummaries == "" {
		generationConfig = nil
	}

	input := any(req.Prompt)
	if len(providerOpts.InputImages) > 0 {
		contents := make([]imageInteractionContent, 0, len(providerOpts.InputImages)+1)
		contents = append(contents, imageInteractionContent{Type: "text", Text: req.Prompt})
		for index, value := range providerOpts.InputImages {
			content, err := imageInteractionContentFromMedia(value)
			if err != nil {
				return nil, fmt.Errorf("google: image: input_images[%d]: %w", index, err)
			}
			contents = append(contents, content)
		}
		input = contents
	}

	apiReq := &imageInteractionRequest{
		Model:                 effectiveOptions.Model,
		Input:                 input,
		ResponseFormat:        responseFormat,
		Store:                 providerOpts.Store,
		GenerationConfig:      generationConfig,
		PreviousInteractionID: providerOpts.PreviousInteractionID,
		Labels:                providerOpts.Labels,
		SafetySettings:        providerOpts.SafetySettings,
		ServiceTier:           providerOpts.ServiceTier,
	}
	if providerOpts.GoogleSearch != nil {
		apiReq.Tools = []imageInteractionTool{{
			Type:        "google_search",
			SearchTypes: slices.Clone(providerOpts.GoogleSearch.SearchTypes),
		}}
	}
	return apiReq, nil
}

func (*ImageModel) validateOptions(options image.Options) error {
	switch {
	case options.Height != nil:
		return errors.New("google: image: height is not supported")
	case options.NegativePrompt != "":
		return errors.New("google: image: negative_prompt is not supported")
	case options.Width != nil:
		return errors.New("google: image: width is not supported")
	default:
		return nil
	}
}

func imageInteractionContentFromMedia(value *media.Media) (imageInteractionContent, error) {
	if err := value.Validate(); err != nil {
		return imageInteractionContent{}, err
	}
	mediaType, _, err := mime.ParseMediaType(value.MIME)
	if err != nil || !strings.HasPrefix(mediaType, imageMediaTypePrefix) {
		return imageInteractionContent{}, fmt.Errorf("MIME type %q is not an image", value.MIME)
	}
	if !slices.Contains([]string{
		mediaTypePNG, mediaTypeJPEG, "image/webp", "image/heic", "image/heif", "image/gif", "image/bmp", "image/tiff",
	}, mediaType) {
		return imageInteractionContent{}, fmt.Errorf("MIME type %q is not supported by the Interactions API", mediaType)
	}
	content := imageInteractionContent{Type: imageInteractionType, MIMEType: mediaType}
	switch value.Source.Kind {
	case media.SourceBytes:
		content.Data = slices.Clone(value.Source.Bytes)
	case media.SourceURI:
		content.URI = value.Source.URI
	default:
		return imageInteractionContent{}, fmt.Errorf("source kind %q is not supported by the Interactions API", value.Source.Kind)
	}
	return content, nil
}

func validateImageSafetySettings(settings []ImageSafetySetting) error {
	for index, setting := range settings {
		if !slices.Contains([]string{
			"hate_speech", "dangerous_content", "harassment", "sexually_explicit",
			"image_hate", "image_dangerous_content", "image_harassment", "image_sexually_explicit", "jailbreak",
		}, setting.Type) {
			return fmt.Errorf("google: image: safety_settings[%d].type: unsupported value %q", index, setting.Type)
		}
		if !slices.Contains([]string{
			"block_low_and_above", "block_medium_and_above", "block_only_high", "block_none", "off",
		}, setting.Threshold) {
			return fmt.Errorf("google: image: safety_settings[%d].threshold: unsupported value %q", index, setting.Threshold)
		}
		if setting.Method != "" && setting.Method != "severity" && setting.Method != "probability" {
			return fmt.Errorf("google: image: safety_settings[%d].method: unsupported value %q", index, setting.Method)
		}
	}
	return nil
}

type imageInteractionResponse struct {
	ID      string            `json:"id"`
	Model   string            `json:"model"`
	Status  string            `json:"status"`
	Object  string            `json:"object"`
	Created string            `json:"created"`
	Updated string            `json:"updated"`
	Steps   []json.RawMessage `json:"steps"`
	Usage   json.RawMessage   `json:"usage"`
	Raw     json.RawMessage   `json:"-"`
}

type imageInteractionStep struct {
	Type    string                     `json:"type"`
	Content []json.RawMessage          `json:"content"`
	Error   *imageInteractionStepError `json:"error,omitempty"`
}

type imageInteractionStepError struct {
	Code    int               `json:"code"`
	Message string            `json:"message"`
	Details []json.RawMessage `json:"details,omitempty"`
}

type imageInteractionOutput struct {
	Type       string `json:"type"`
	Data       []byte `json:"data,omitempty"`
	URI        string `json:"uri,omitempty"`
	MIMEType   string `json:"mime_type,omitempty"`
	Resolution string `json:"resolution,omitempty"`
}

func (i imageInteractionOutput) media() (*media.Media, error) {
	if i.MIMEType == "" {
		return nil, errors.New("image output has no MIME type")
	}
	switch {
	case len(i.Data) > 0 && i.URI == "":
		return media.NewBytes(i.MIMEType, i.Data)
	case len(i.Data) == 0 && i.URI != "":
		return media.NewURI(i.MIMEType, i.URI)
	case len(i.Data) == 0:
		return nil, errors.New("image output has neither inline data nor URI")
	default:
		return nil, errors.New("image output has both inline data and URI")
	}
}

func (i *ImageModel) buildResponse(apiResp *imageInteractionResponse) (*image.Response, error) {
	if apiResp == nil {
		return nil, errors.New("google: image: nil Interactions response")
	}
	var outputs []*image.Output
	for stepIndex, rawStep := range apiResp.Steps {
		var step imageInteractionStep
		if err := json.Unmarshal(rawStep, &step); err != nil {
			return nil, fmt.Errorf("google: image: decode steps[%d]: %w", stepIndex, err)
		}
		if step.Error != nil {
			return nil, fmt.Errorf("google: image: steps[%d] failed with code %d: %s", stepIndex, step.Error.Code, step.Error.Message)
		}
		if step.Type != "model_output" {
			continue
		}
		for contentIndex, rawContent := range step.Content {
			var interactionOutput imageInteractionOutput
			if err := json.Unmarshal(rawContent, &interactionOutput); err != nil {
				return nil, fmt.Errorf("google: image: decode steps[%d].content[%d]: %w", stepIndex, contentIndex, err)
			}
			if interactionOutput.Type != imageInteractionType {
				continue
			}
			value, err := interactionOutput.media()
			if err != nil {
				return nil, fmt.Errorf("google: image: steps[%d].content[%d]: %w", stepIndex, contentIndex, err)
			}
			var outputMetadata metadata.Map
			if setErr := outputMetadata.Set("google/image_content", rawContent); setErr != nil {
				return nil, setErr
			}
			output, err := image.NewOutput(value, outputMetadata)
			if err != nil {
				return nil, err
			}
			outputs = append(outputs, output)
		}
	}
	if len(outputs) == 0 {
		return nil, fmt.Errorf("google: image: Interactions response status %q has no model-output images", apiResp.Status)
	}

	meta := &image.ResponseMetadata{}
	if apiResp.Created != "" {
		created, err := time.Parse(time.RFC3339, apiResp.Created)
		if err != nil {
			return nil, fmt.Errorf("google: image: invalid provider creation time %q: %w", apiResp.Created, err)
		}
		meta.CreatedAt = created.UTC()
	}
	if err := meta.Extra.Set(ImageResponseExtensionKey, apiResp.Raw); err != nil {
		return nil, err
	}
	return image.NewResponse(outputs, meta)
}

func (i *ImageModel) Call(ctx context.Context, req *image.Request) (*image.Response, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	apiReq, err := i.buildAPIRequest(req)
	if err != nil {
		return nil, err
	}

	apiResp, err := i.api.createImageInteraction(ctx, apiReq)
	if err != nil {
		return nil, err
	}
	return i.buildResponse(apiResp)
}
