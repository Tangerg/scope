package google

import (
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/media"
)

const (
	imageInteractionType             = "image"
	imageInteractionTextType         = "text"
	imageInteractionGoogleSearchType = "google_search"
	mediaTypePNG                     = "image/png"
	mediaTypeJPEG                    = "image/jpeg"
	mediaTypeWebP                    = "image/webp"
	mediaTypeHEIC                    = "image/heic"
	mediaTypeHEIF                    = "image/heif"
	mediaTypeGIF                     = "image/gif"
	mediaTypeBMP                     = "image/bmp"
	mediaTypeTIFF                    = "image/tiff"
)

type imageInteractionRequest struct {
	Model                 string                            `json:"model"`
	Input                 any                               `json:"input"`
	Tools                 []imageInteractionTool            `json:"tools,omitempty"`
	ResponseFormat        imageInteractionResponseFormat    `json:"response_format"`
	Store                 *bool                             `json:"store,omitzero"`
	GenerationConfig      *imageInteractionGenerationConfig `json:"generation_config,omitzero"`
	PreviousInteractionID string                            `json:"previous_interaction_id,omitempty"`
	Labels                map[string]string                 `json:"labels,omitempty"`
	SafetySettings        []ImageSafetySetting              `json:"safety_settings,omitempty"`
	ServiceTier           ImageServiceTier                  `json:"service_tier,omitempty"`
}

type imageInteractionContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     []byte `json:"data,omitempty"`
	URI      string `json:"uri,omitempty"`
	MIMEType string `json:"mime_type,omitempty"`
}

type imageInteractionTool struct {
	Type        string            `json:"type"`
	SearchTypes []ImageSearchType `json:"search_types,omitempty"`
}

type imageInteractionResponseFormat struct {
	Type        string           `json:"type"`
	MIMEType    string           `json:"mime_type,omitempty"`
	AspectRatio ImageAspectRatio `json:"aspect_ratio,omitempty"`
	ImageSize   ImageSize        `json:"image_size,omitempty"`
	Delivery    ImageDelivery    `json:"delivery,omitempty"`
}

type imageInteractionGenerationConfig struct {
	Seed              *int32                   `json:"seed,omitzero"`
	ThinkingLevel     ImageThinkingLevel       `json:"thinking_level,omitempty"`
	ThinkingSummaries ImageThinkingSummaryMode `json:"thinking_summaries,omitempty"`
}

func newImageInteractionRequest(req *image.Request, defaults image.Options) (*imageInteractionRequest, error) {
	effectiveOptions, err := defaults.Resolve(req.Options)
	if err != nil {
		return nil, err
	}
	providerOpts, err := admitImageOptions(effectiveOptions)
	if err != nil {
		return nil, err
	}

	responseFormat := imageInteractionResponseFormat{
		Type:        imageInteractionType,
		MIMEType:    string(effectiveOptions.OutputFormat),
		AspectRatio: providerOpts.AspectRatio,
		ImageSize:   providerOpts.ImageSize,
		Delivery:    providerOpts.Delivery,
	}
	generationConfig := &imageInteractionGenerationConfig{
		ThinkingLevel:     providerOpts.ThinkingLevel,
		ThinkingSummaries: providerOpts.ThinkingSummaries,
	}
	if effectiveOptions.Seed != nil {
		generationConfig.Seed = new(int32(*effectiveOptions.Seed))
	}
	if generationConfig.Seed == nil && generationConfig.ThinkingLevel == "" && generationConfig.ThinkingSummaries == "" {
		generationConfig = nil
	}

	input := any(req.Prompt)
	if len(providerOpts.InputImages) > 0 {
		contents := make([]imageInteractionContent, 0, len(providerOpts.InputImages)+1)
		contents = append(contents, imageInteractionContent{Type: imageInteractionTextType, Text: req.Prompt})
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
			Type:        imageInteractionGoogleSearchType,
			SearchTypes: slices.Clone(providerOpts.GoogleSearch.SearchTypes),
		}}
	}
	return apiReq, nil
}

// admitImageOptions admits the options alone and returns their native
// controls. A binding applies it to its defaults at construction and to every
// resolved request, so defaults no request could send fail when they are
// configured.
func admitImageOptions(options image.Options) (*ImageGenerationOptions, error) {
	if err := validateImageOptions(options); err != nil {
		return nil, err
	}
	providerOpts, _, err := options.Extensions.Decode[ImageGenerationOptions](ImageRequestExtensionKey)
	if err != nil {
		return nil, err
	}
	if err := providerOpts.validate(options.Model); err != nil {
		return nil, err
	}
	if options.OutputFormat != "" && options.OutputFormat != mediaTypePNG && options.OutputFormat != mediaTypeJPEG {
		return nil, fmt.Errorf("google: image: unsupported output format %q; use %s or %s",
			options.OutputFormat, mediaTypePNG, mediaTypeJPEG)
	}
	if options.Seed != nil && *options.Seed > int64(math.MaxInt32) {
		return nil, fmt.Errorf("google: image: seed exceeds int32: %d", *options.Seed)
	}
	for index, value := range providerOpts.InputImages {
		if _, err := imageInteractionContentFromMedia(value); err != nil {
			return nil, fmt.Errorf("google: image: input_images[%d]: %w", index, err)
		}
	}
	return &providerOpts, nil
}

func validateImageOptions(options image.Options) error {
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
	if err := value.RequireMIME(); err != nil {
		return imageInteractionContent{}, err
	}
	mediaType := value.Type()
	if mediaType.TopLevel() != media.TopLevelImage {
		return imageInteractionContent{}, fmt.Errorf("MIME type %q is not an image", value.MIME)
	}
	if !slices.Contains([]media.Type{
		mediaTypePNG, mediaTypeJPEG, mediaTypeWebP, mediaTypeHEIC, mediaTypeHEIF, mediaTypeGIF, mediaTypeBMP, mediaTypeTIFF,
	}, mediaType) {
		return imageInteractionContent{}, fmt.Errorf("MIME type %q is not supported by the Interactions API", mediaType)
	}
	content := imageInteractionContent{Type: imageInteractionType, MIMEType: string(mediaType)}
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
