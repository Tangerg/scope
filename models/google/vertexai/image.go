package vertexai

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"math"

	"google.golang.org/genai"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/models/google/internal/protocol"
)

type ImageModelConfig struct {
	Client         ClientConfig
	DefaultOptions image.Options
}

func (i ImageModelConfig) Validate() error {
	return i.Client.validateModelOptions(i.DefaultOptions.Model, i.DefaultOptions)
}

const (
	vertexAPIVersion           = "v1"
	mediaTypePNG               = "image/png"
	mediaTypeJPEG              = "image/jpeg"
	imageNativePartMetadataKey = Provider + "/native_part"
)

// ImagePersonGeneration uses GenerateContent's vocabulary. Imagen's similarly
// named SDK enum uses a different spelling for blocking people.
type ImagePersonGeneration string

const (
	ImagePersonGenerationUnspecified ImagePersonGeneration = "PERSON_GENERATION_UNSPECIFIED"
	ImagePersonGenerationAllowAll    ImagePersonGeneration = "ALLOW_ALL"
	ImagePersonGenerationAllowAdult  ImagePersonGeneration = "ALLOW_ADULT"
	ImagePersonGenerationAllowNone   ImagePersonGeneration = "ALLOW_NONE"
)

func (i ImagePersonGeneration) validate() error {
	switch i {
	case "", ImagePersonGenerationUnspecified, ImagePersonGenerationAllowAll, ImagePersonGenerationAllowAdult, ImagePersonGenerationAllowNone:
		return nil
	default:
		return fmt.Errorf("vertexai: image: unsupported person_generation %q", i)
	}
}

// ImageGenerationOptions carries Vertex-specific GenerateContent controls and
// optional source images for editing. Store it under
// [ImageRequestExtensionKey].
type ImageGenerationOptions struct {
	InputImages              []*media.Media        `json:"input_images,omitempty"`
	AspectRatio              string                `json:"aspect_ratio,omitempty"`
	ImageSize                string                `json:"image_size,omitempty"`
	PersonGeneration         ImagePersonGeneration `json:"person_generation,omitempty"`
	ProminentPeople          genai.ProminentPeople `json:"prominent_people,omitempty"`
	OutputCompressionQuality *int32                `json:"output_compression_quality,omitzero"`
}

func (i *ImageGenerationOptions) UnmarshalJSON(data []byte) error {
	type wireOptions ImageGenerationOptions
	var decoded *wireOptions
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return err
	}
	if decoded == nil {
		return errors.New("vertexai: image options must be an object")
	}
	value := ImageGenerationOptions(*decoded)
	if err := value.validate(); err != nil {
		return err
	}
	*i = value
	return nil
}

func (i ImageGenerationOptions) validate() error {
	if err := i.PersonGeneration.validate(); err != nil {
		return err
	}
	switch i.ProminentPeople {
	case "", genai.ProminentPeopleUnspecified, genai.ProminentPeopleAllowProminentPeople, genai.ProminentPeopleBlockProminentPeople:
		return nil
	default:
		return fmt.Errorf("vertexai: image: unsupported prominent_people %q", i.ProminentPeople)
	}
}

var _ image.Model = (*ImageModel)(nil)

// ImageModel implements Gemini native image generation on Vertex AI through
// GenerateContent. It intentionally does not expose Imagen's Predict endpoint:
// Google deprecated the Imagen GA endpoints in favor of Gemini image models.
type ImageModel struct {
	client         *protocol.GenerationClient
	defaultOptions image.Options
}

func NewImageModel(ctx context.Context, config ImageModelConfig) (*ImageModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	clientConfig := config.Client.protocol()
	clientConfig.APIVersion = vertexAPIVersion
	client, err := protocol.NewGenerationClient(ctx, clientConfig)
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
			PersonGeneration:         string(providerOpts.PersonGeneration),
			ProminentPeople:          providerOpts.ProminentPeople,
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
		config.ImageConfig.OutputMIMEType = string(effectiveOptions.OutputFormat)
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
	mediaType := value.Type()
	if mediaType.TopLevel() != media.TopLevelImage {
		return nil, fmt.Errorf("MIME type %q is not an image", value.MIME)
	}
	switch value.Source.Kind {
	case media.SourceBytes:
		return genai.NewPartFromBytes(value.Source.Bytes, string(mediaType)), nil
	case media.SourceURI:
		return genai.NewPartFromURI(value.Source.URI, string(mediaType)), nil
	default:
		return nil, fmt.Errorf("source kind %q is not supported by Vertex GenerateContent", value.Source.Kind)
	}
}

// declaresImage selects image parts; GenerateContent may interleave others.
func declaresImage(declaration string) bool {
	mediaType, err := media.ParseType(declaration)
	return err == nil && mediaType.TopLevel() == media.TopLevelImage
}

func (i *ImageModel) buildResponse(apiResp *genai.GenerateContentResponse) (*image.Response, error) {
	if apiResp == nil {
		return nil, errors.New("vertexai: image: nil GenerateContent response")
	}
	if err := protocol.ValidateGenerationCompletion(apiResp); err != nil {
		return nil, fmt.Errorf("vertexai: image: %w", err)
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
			case part.InlineData != nil && declaresImage(part.InlineData.MIMEType):
				value, err = media.NewBytes(part.InlineData.MIMEType, part.InlineData.Data)
			case part.FileData != nil && declaresImage(part.FileData.MIMEType):
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
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: errors.New("vertexai: image: nil model")}
	}
	if err := req.Validate(); err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}
	modelName, contents, config, err := i.buildRequest(req)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}
	apiResp, err := i.client.GenerateContent(ctx, modelName, contents, config)
	if err != nil {
		if errors.Is(err, protocol.ErrInvalidGenerationResponse) {
			return nil, fmt.Errorf("%w: %w", image.ErrInvalidResponse, err)
		}
		return nil, fmt.Errorf("vertexai: image: GenerateContent: %w", err)
	}
	return i.buildResponse(apiResp)
}
