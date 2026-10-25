package replicate

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
)

type ImageModelConfig struct {
	APIKey         string
	DefaultOptions image.Options
	InputSchema    ImageInputSchema
	BaseURL        string
	HTTPClient     *http.Client

	// PollInterval / PollTimeout configure the synchronous wrapper
	// around Replicate's async generation. Zero values fall back to
	// the package defaults.
	PollInterval time.Duration
	PollTimeout  time.Duration
}

func (i ImageModelConfig) Validate() error {
	if i.APIKey == "" {
		return errors.New("replicate: APIKey is required")
	}
	if i.DefaultOptions.Model == "" {
		return errors.New("replicate: DefaultOptions.Model is required")
	}
	if err := i.DefaultOptions.Validate(); err != nil {
		return err
	}
	if err := i.InputSchema.Validate(); err != nil {
		return err
	}
	if i.PollInterval < 0 {
		return errors.New("replicate: PollInterval must not be negative")
	}
	if i.PollTimeout < 0 {
		return errors.New("replicate: PollTimeout must not be negative")
	}
	return nil
}

// FileOutputKind identifies the exact file-output schema declared by a
// Replicate model version.
type FileOutputKind string

// The vocabulary is closed because it decides how a prediction's output is
// read. Guessing between a single URI and a list would either drop results or
// misreport one as many, so the shape is declared rather than inferred.
const (
	FileOutputURI     FileOutputKind = "uri"
	FileOutputURIList FileOutputKind = "uri_list"
)

// imageURLs extracts every hosted image URL from a Replicate prediction.
func (f FileOutputKind) imageURLs(out any) ([]string, error) {
	if out == nil {
		return nil, errors.New("replicate: image output is null")
	}
	switch f {
	case FileOutputURI:
		value, ok := out.(string)
		if !ok || value == "" {
			return nil, fmt.Errorf("replicate: image output must be a non-empty URI, got %T", out)
		}
		return []string{value}, nil
	case FileOutputURIList:
		values, ok := out.([]any)
		if !ok || len(values) == 0 {
			return nil, fmt.Errorf("replicate: image output must be a non-empty URI array, got %T", out)
		}
		urls := make([]string, len(values))
		for index, value := range values {
			url, ok := value.(string)
			if !ok || url == "" {
				return nil, fmt.Errorf("replicate: image output[%d] must be a non-empty URI, got %T", index, value)
			}
			urls[index] = url
		}
		return urls, nil
	default:
		return nil, fmt.Errorf("replicate: unsupported image output schema %q", f)
	}
}

func (f FileOutputKind) audioURL(out any) (string, error) {
	if out == nil {
		return "", errors.New("replicate: speech output is null")
	}
	switch f {
	case FileOutputURI:
		value, ok := out.(string)
		if !ok || value == "" {
			return "", fmt.Errorf("replicate: speech output must be a non-empty URI, got %T", out)
		}
		return value, nil
	case FileOutputURIList:
		values, ok := out.([]any)
		if !ok || len(values) != 1 {
			return "", fmt.Errorf("replicate: speech output must be a one-element URI array, got %T", out)
		}
		value, ok := values[0].(string)
		if !ok || value == "" {
			return "", fmt.Errorf("replicate: speech output[0] must be a non-empty URI, got %T", values[0])
		}
		return value, nil
	default:
		return "", fmt.Errorf("replicate: unsupported speech output schema %q", f)
	}
}

// ImageInputSchema explicitly binds provider-neutral image fields to one
// Replicate model's OpenAPI input/output schema. Empty optional keys mean the
// model cannot represent that Core option; setting the option then fails
// instead of guessing a similarly named provider field.
type ImageInputSchema struct {
	PromptKey         string
	NegativePromptKey string
	WidthKey          string
	HeightKey         string
	SeedKey           string
	OutputFormatKey   string
	OutputFormats     map[string]string
	OutputKind        FileOutputKind
}

func (i ImageInputSchema) Clone() ImageInputSchema {
	i.OutputFormats = maps.Clone(i.OutputFormats)
	return i
}

func (i ImageInputSchema) Validate() error {
	if i.PromptKey == "" {
		return errors.New("replicate: ImageInputSchema.PromptKey is required")
	}
	if i.OutputKind != FileOutputURI && i.OutputKind != FileOutputURIList {
		return fmt.Errorf("replicate: ImageInputSchema.OutputKind must be %q or %q", FileOutputURI, FileOutputURIList)
	}
	if i.OutputFormatKey == "" && len(i.OutputFormats) > 0 {
		return errors.New("replicate: ImageInputSchema.OutputFormats requires OutputFormatKey")
	}
	if i.OutputFormatKey != "" && len(i.OutputFormats) == 0 {
		return errors.New("replicate: ImageInputSchema.OutputFormatKey requires OutputFormats")
	}
	for mimeType, value := range i.OutputFormats {
		if !strings.HasPrefix(mimeType, mediaTypeImagePrefix) || value == "" {
			return fmt.Errorf("replicate: ImageInputSchema.OutputFormats contains invalid mapping %q -> %q", mimeType, value)
		}
	}
	keys := []string{i.PromptKey, i.NegativePromptKey, i.WidthKey, i.HeightKey, i.SeedKey, i.OutputFormatKey}
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if key == "" {
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("replicate: ImageInputSchema maps multiple fields to input key %q", key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func (i ImageInputSchema) validateOptions(options image.Options) error {
	var unsupported []string
	if options.Height != nil && i.HeightKey == "" {
		unsupported = append(unsupported, fieldHeight)
	}
	if options.NegativePrompt != "" && i.NegativePromptKey == "" {
		unsupported = append(unsupported, fieldNegativePrompt)
	}
	if options.OutputFormat != "" && i.OutputFormatKey == "" {
		unsupported = append(unsupported, fieldOutputFormat)
	}
	if options.Seed != nil && i.SeedKey == "" {
		unsupported = append(unsupported, fieldSeed)
	}
	if options.Width != nil && i.WidthKey == "" {
		unsupported = append(unsupported, fieldWidth)
	}
	if len(unsupported) == 0 {
		return nil
	}
	return fmt.Errorf("replicate: image: unsupported options: %s", strings.Join(unsupported, ", "))
}

// FluxSchnellImageInputSchema returns the current official schema binding for
// black-forest-labs/flux-schnell. Dimensions, seed, and negative prompt are
// intentionally absent because that model's current schema does not expose
// them.
func FluxSchnellImageInputSchema() ImageInputSchema {
	return ImageInputSchema{
		PromptKey:       fieldPrompt,
		OutputFormatKey: fieldOutputFormat,
		OutputFormats: map[string]string{
			mediaTypeJPEG: outputFormatJPG,
			mediaTypePNG:  outputFormatPNG,
			mediaTypeWebP: outputFormatWebP,
		},
		OutputKind: FileOutputURIList,
	}
}

var _ image.Model = (*ImageModel)(nil)

// ImageModel wraps one explicitly bound Replicate image-model schema. A model
// override is rejected because Replicate models do not share an input or
// output contract; construct another adapter with the matching schema instead.
type ImageModel struct {
	predictions    predictionRunner
	model          string
	inputSchema    ImageInputSchema
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
		predictions:    newPredictionRunner(api, config.PollInterval, config.PollTimeout, time.Duration(DefaultPollTimeoutSeconds)*time.Second),
		model:          config.DefaultOptions.Model,
		inputSchema:    config.InputSchema.Clone(),
		defaultOptions: config.DefaultOptions.Clone(),
	}, nil
}

func (i *ImageModel) Call(ctx context.Context, req *image.Request) (*image.Response, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	effectiveOptions, apiRequest, err := i.prepareRequest(req)
	if err != nil {
		return nil, err
	}
	final, err := i.predictions.run(ctx, effectiveOptions.Model, apiRequest)
	if err != nil {
		return nil, err
	}
	return i.response(ctx, effectiveOptions, final)
}

func (i *ImageModel) prepareRequest(req *image.Request) (image.Options, *predictionRequest, error) {
	effectiveOptions, err := i.defaultOptions.Resolve(req.Options)
	if err != nil {
		return image.Options{}, nil, err
	}
	if effectiveOptions.Model != i.model {
		return image.Options{}, nil, fmt.Errorf("replicate: image: model override %q does not match bound schema for %q", effectiveOptions.Model, i.model)
	}
	if validateOptionsErr := i.inputSchema.validateOptions(effectiveOptions); validateOptionsErr != nil {
		return image.Options{}, nil, validateOptionsErr
	}
	apiReqValue, _, err := effectiveOptions.Extensions.Decode[predictionRequest](ImageRequestExtensionKey)
	if err != nil {
		return image.Options{}, nil, err
	}
	apiReq := &apiReqValue
	for _, key := range []string{i.inputSchema.PromptKey, i.inputSchema.NegativePromptKey, i.inputSchema.WidthKey, i.inputSchema.HeightKey, i.inputSchema.SeedKey, i.inputSchema.OutputFormatKey} {
		if key != "" {
			if _, exists := apiReq.Input[key]; exists {
				return image.Options{}, nil, fmt.Errorf("replicate: extension input %q is owned by Core", key)
			}
		}
	}
	var inputErr error
	setInput := func(key string, value any) {
		if inputErr == nil {
			inputErr = apiReq.setInput(key, value)
		}
	}
	setInput(i.inputSchema.PromptKey, req.Prompt)
	if effectiveOptions.NegativePrompt != "" {
		setInput(i.inputSchema.NegativePromptKey, effectiveOptions.NegativePrompt)
	}
	if effectiveOptions.Width != nil {
		setInput(i.inputSchema.WidthKey, *effectiveOptions.Width)
	}
	if effectiveOptions.Height != nil {
		setInput(i.inputSchema.HeightKey, *effectiveOptions.Height)
	}
	if effectiveOptions.Seed != nil {
		setInput(i.inputSchema.SeedKey, *effectiveOptions.Seed)
	}
	if effectiveOptions.OutputFormat != "" {
		value, supported := i.inputSchema.OutputFormats[effectiveOptions.OutputFormat]
		if !supported {
			return image.Options{}, nil, fmt.Errorf("replicate: image: model %q does not support output format %q", i.model, effectiveOptions.OutputFormat)
		}
		setInput(i.inputSchema.OutputFormatKey, value)
	}
	if inputErr != nil {
		return image.Options{}, nil, inputErr
	}
	return effectiveOptions, apiReq, nil
}

func (i *ImageModel) response(ctx context.Context, effectiveOptions image.Options, final *predictionResponse) (*image.Response, error) {
	urls, err := i.inputSchema.OutputKind.imageURLs(final.Output)
	if err != nil {
		return nil, err
	}

	outputs, err := i.downloadOutputs(ctx, urls, effectiveOptions.OutputFormat, final)
	if err != nil {
		return nil, err
	}

	meta := &image.ResponseMetadata{}
	if final.CreatedAt != "" {
		createdAt, err := time.Parse(time.RFC3339Nano, final.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("replicate: invalid prediction created_at %q: %w", final.CreatedAt, err)
		}
		meta.CreatedAt = createdAt.UTC()
	}
	if err := meta.Extra.Set(metadataModel, effectiveOptions.Model); err != nil {
		return nil, err
	}
	if err := meta.Extra.Set(metadataPredictionID, final.ID); err != nil {
		return nil, err
	}
	if final.Version != "" {
		if err := meta.Extra.Set(metadataVersion, final.Version); err != nil {
			return nil, err
		}
	}
	if err := meta.Extra.Set(ImageResponseExtensionKey, final); err != nil {
		return nil, err
	}
	return image.NewResponse(outputs, meta)
}

func (i *ImageModel) downloadOutputs(ctx context.Context, urls []string, fallbackMIMEType string, final *predictionResponse) ([]*image.Output, error) {
	outputs := make([]*image.Output, 0, len(urls))
	for outputIndex, outputURL := range urls {
		output, err := i.downloadOutput(ctx, outputURL, fallbackMIMEType, final)
		if err != nil {
			return nil, fmt.Errorf("replicate: image output[%d]: %w", outputIndex, err)
		}
		outputs = append(outputs, output)
	}
	return outputs, nil
}

func (i *ImageModel) downloadOutput(ctx context.Context, outputURL, fallbackMIMEType string, final *predictionResponse) (*image.Output, error) {
	data, contentType, err := i.predictions.api.downloadOutput(ctx, outputURL)
	if err != nil {
		return nil, err
	}
	mimeType, err := outputMIMEType(contentType, fallbackMIMEType)
	if err != nil {
		return nil, err
	}
	value, err := media.NewBytes(mimeType, data)
	if err != nil {
		return nil, err
	}
	var outputMetadata metadata.Map
	if err := outputMetadata.Set(metadataOutputURL, outputURL); err != nil {
		return nil, err
	}
	if milliseconds, present := final.predictTimeMilliseconds(); present {
		if err := outputMetadata.Set(metadataPredictTimeMilliseconds, milliseconds); err != nil {
			return nil, err
		}
	}
	return image.NewOutput(value, outputMetadata)
}

func outputMIMEType(contentType, fallback string) (string, error) {
	contentType = strings.TrimSpace(contentType)
	if contentType == "" {
		return cmp.Or(fallback, mediaTypeBinary), nil
	}
	mimeType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", fmt.Errorf("parse content type %q: %w", contentType, err)
	}
	return mimeType, nil
}
