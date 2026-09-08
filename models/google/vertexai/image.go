package vertexai

import (
	"context"
	"errors"
	"fmt"
	"math"
	"mime"
	"strings"

	"google.golang.org/genai"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
)

// ImageModelConfig binds provider access and defaults shared by every image call.
type ImageModelConfig struct {
	Client         ClientConfig
	DefaultOptions image.Options
}

func (i ImageModelConfig) Validate() error {
	return i.Client.validateModelOptions(i.DefaultOptions.Model, i.DefaultOptions)
}

const (
	vertexAPIVersion           = "v1"
	imageMediaTypePrefix       = "image/"
	mediaTypePNG               = "image/png"
	mediaTypeJPEG              = "image/jpeg"
	imageNativePartMetadataKey = "vertexai/native_part"
)

// ImageGenerationOptions carries Vertex-specific GenerateContent controls and
// optional source images for editing. Store it under
// [ImageRequestExtensionKey].
type ImageGenerationOptions struct {
	InputImages              []*media.Media `json:"input_images,omitempty"`
	AspectRatio              string         `json:"aspect_ratio,omitempty"`
	ImageSize                string         `json:"image_size,omitempty"`
	PersonGeneration         string         `json:"person_generation,omitempty"`
	ProminentPeople          string         `json:"prominent_people,omitempty"`
	OutputCompressionQuality *int32         `json:"output_compression_quality,omitempty"`
}

var _ image.Model = (*ImageModel)(nil)

// ImageModel implements Gemini native image generation on Vertex AI through
// GenerateContent. It intentionally does not expose Imagen's Predict endpoint:
// Google deprecated the Imagen GA endpoints in favor of Gemini image models.
type ImageModel struct {
	client         *genai.Client
	defaultOptions image.Options
}

// NewImageModel rejects an invalid provider binding before the first image call.
func NewImageModel(ctx context.Context, config ImageModelConfig) (*ImageModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	clientConfig := &genai.ClientConfig{
		Backend:    genai.BackendVertexAI,
		Project:    config.Client.Project,
		Location:   config.Client.Location,
		HTTPClient: config.Client.HTTPClient,
		HTTPOptions: genai.HTTPOptions{
			APIVersion: vertexAPIVersion,
			BaseURL:    config.Client.BaseURL,
		},
	}
	client, err := genai.NewClient(ctx, clientConfig)
	if err != nil {
		return nil, fmt.Errorf("vertexai: create Gen AI client: %w", err)
	}
	return &ImageModel{client: client, defaultOptions: config.DefaultOptions.Clone()}, nil
}

func (i *ImageModel) buildRequest(req *image.Request) (string, []*genai.Content, *genai.GenerateContentConfig, error) {
	effectiveOptions, err := i.defaultOptions.Resolve(req.Options)
	if err != nil {
		return "", nil, nil, err
	}
	if validateOptionsErr := i.validateOptions(effectiveOptions); validateOptionsErr != nil {
		return "", nil, nil, validateOptionsErr
	}

	providerOptsValue, _, err := effectiveOptions.Extensions.Decode[ImageGenerationOptions](ImageRequestExtensionKey)

	providerOpts := &providerOptsValue
	if err != nil {
		return "", nil, nil, err
	}
	config := genai.GenerateContentConfig{
		ResponseModalities: []string{string(genai.ModalityImage)},
		ImageConfig: &genai.ImageConfig{
			AspectRatio:              providerOpts.AspectRatio,
			ImageSize:                providerOpts.ImageSize,
			PersonGeneration:         providerOpts.PersonGeneration,
			ProminentPeople:          genai.ProminentPeople(providerOpts.ProminentPeople),
			OutputCompressionQuality: providerOpts.OutputCompressionQuality,
		},
	}
	if effectiveOptions.Seed != nil {
		if *effectiveOptions.Seed > math.MaxInt32 {
			return "", nil, nil, fmt.Errorf("vertexai: image: seed exceeds int32: %d", *effectiveOptions.Seed)
		}
		config.Seed = new(int32(*effectiveOptions.Seed))
	}
	if effectiveOptions.OutputFormat != "" {
		if effectiveOptions.OutputFormat != mediaTypePNG && effectiveOptions.OutputFormat != mediaTypeJPEG {
			return "", nil, nil, fmt.Errorf("vertexai: image: unsupported output format %q; use %s or %s",
				effectiveOptions.OutputFormat, mediaTypePNG, mediaTypeJPEG)
		}
		if config.ImageConfig == nil {
			config.ImageConfig = &genai.ImageConfig{}
		}
		config.ImageConfig.OutputMIMEType = effectiveOptions.OutputFormat
	}

	parts := make([]*genai.Part, 0, len(providerOpts.InputImages)+1)
	parts = append(parts, genai.NewPartFromText(req.Prompt))
	for index, value := range providerOpts.InputImages {
		part, err := vertexImagePart(value)
		if err != nil {
			return "", nil, nil, fmt.Errorf("vertexai: image: input_images[%d]: %w", index, err)
		}
		parts = append(parts, part)
	}
	contents := []*genai.Content{genai.NewContentFromParts(parts, genai.RoleUser)}
	return effectiveOptions.Model, contents, &config, nil
}

func (*ImageModel) validateOptions(options image.Options) error {
	switch {
	case options.Height != nil:
		return errors.New("vertexai: image: height is not supported")
	case options.NegativePrompt != "":
		return errors.New("vertexai: image: negative_prompt is not supported")
	case options.Width != nil:
		return errors.New("vertexai: image: width is not supported")
	default:
		return nil
	}
}

func vertexImagePart(value *media.Media) (*genai.Part, error) {
	if err := value.Validate(); err != nil {
		return nil, err
	}
	mediaType, _, err := mime.ParseMediaType(value.MIME)
	if err != nil || !strings.HasPrefix(mediaType, imageMediaTypePrefix) {
		return nil, fmt.Errorf("MIME type %q is not an image", value.MIME)
	}
	switch value.Source.Kind {
	case media.SourceBytes:
		return genai.NewPartFromBytes(value.Source.Bytes, mediaType), nil
	case media.SourceURI:
		return genai.NewPartFromURI(value.Source.URI, mediaType), nil
	default:
		return nil, fmt.Errorf("source kind %q is not supported by Vertex GenerateContent", value.Source.Kind)
	}
}

func (i *ImageModel) buildResponse(apiResp *genai.GenerateContentResponse) (*image.Response, error) {
	if apiResp == nil {
		return nil, errors.New("vertexai: image: nil GenerateContent response")
	}
	var outputs []*image.Output
	for candidateIndex, candidate := range apiResp.Candidates {
		if candidate == nil || candidate.Content == nil {
			continue
		}
		for partIndex, part := range candidate.Content.Parts {
			if part == nil || part.Thought {
				continue
			}
			var value *media.Media
			var err error
			switch {
			case part.InlineData != nil && strings.HasPrefix(part.InlineData.MIMEType, imageMediaTypePrefix):
				value, err = media.NewBytes(part.InlineData.MIMEType, part.InlineData.Data)
			case part.FileData != nil && strings.HasPrefix(part.FileData.MIMEType, imageMediaTypePrefix):
				value, err = media.NewURI(part.FileData.MIMEType, part.FileData.FileURI)
			default:
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("vertexai: image: candidates[%d].parts[%d]: %w", candidateIndex, partIndex, err)
			}
			var outputMetadata metadata.Map
			if setErr := outputMetadata.Set(imageNativePartMetadataKey, part); setErr != nil {
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
		return nil, errors.New("vertexai: image: GenerateContent response has no final image parts")
	}

	metadata := &image.ResponseMetadata{}
	if !apiResp.CreateTime.IsZero() {
		metadata.CreatedAt = apiResp.CreateTime.UTC()
	}
	if err := metadata.Extra.Set(ImageResponseExtensionKey, apiResp); err != nil {
		return nil, err
	}
	return image.NewResponse(outputs, metadata)
}

func (i *ImageModel) Call(ctx context.Context, req *image.Request) (*image.Response, error) {
	if i == nil || i.client == nil {
		return nil, errors.New("vertexai: image: nil model")
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}
	modelName, contents, config, err := i.buildRequest(req)
	if err != nil {
		return nil, err
	}
	apiResp, err := i.client.Models.GenerateContent(ctx, modelName, contents, config)
	if err != nil {
		return nil, fmt.Errorf("vertexai: image: GenerateContent: %w", err)
	}
	return i.buildResponse(apiResp)
}
