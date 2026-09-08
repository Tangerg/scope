package replicate

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Tangerg/scope/core/metadata"
	tts "github.com/Tangerg/scope/core/speech"
)

// AudioTTSModelConfig binds provider access and defaults shared by every speech call.
type AudioTTSModelConfig struct {
	APIKey         string
	DefaultOptions tts.Options
	InputSchema    SpeechInputSchema
	BaseURL        string
	HTTPClient     *http.Client

	// PollInterval / PollTimeout configure the synchronous wrapper
	// around Replicate's async generation. Zero values fall back to
	// the package defaults; community TTS jobs can include cold-start
	// latency, so PollTimeout defaults higher than image.
	PollInterval time.Duration
	PollTimeout  time.Duration
}

func (a AudioTTSModelConfig) Validate() error {
	if a.APIKey == "" {
		return errors.New("replicate: APIKey is required")
	}
	if a.DefaultOptions.Model == "" {
		return errors.New("replicate: DefaultOptions.Model is required")
	}
	if err := a.DefaultOptions.Validate(); err != nil {
		return err
	}
	if err := a.InputSchema.Validate(); err != nil {
		return err
	}
	if a.PollInterval < 0 {
		return errors.New("replicate: PollInterval must not be negative")
	}
	if a.PollTimeout < 0 {
		return errors.New("replicate: PollTimeout must not be negative")
	}
	return nil
}

// SpeechInputSchema explicitly binds Core speech fields to one Replicate
// model version's OpenAPI schema.
type SpeechInputSchema struct {
	TextKey       string
	VoiceKey      string
	SpeedKey      string
	VoiceRequired bool
	OutputKind    FileOutputKind
}

func (s SpeechInputSchema) Validate() error {
	if s.TextKey == "" {
		return errors.New("replicate: SpeechInputSchema.TextKey is required")
	}
	if s.VoiceRequired && s.VoiceKey == "" {
		return errors.New("replicate: SpeechInputSchema.VoiceRequired requires VoiceKey")
	}
	if s.OutputKind != FileOutputURI && s.OutputKind != FileOutputURIList {
		return fmt.Errorf("replicate: SpeechInputSchema.OutputKind must be %q or %q", FileOutputURI, FileOutputURIList)
	}
	keys := []string{s.TextKey, s.VoiceKey, s.SpeedKey}
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if key == "" {
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("replicate: SpeechInputSchema maps multiple fields to input key %q", key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func (s SpeechInputSchema) validateOptions(options tts.Options) error {
	var unsupported []string
	if options.OutputFormat != "" {
		unsupported = append(unsupported, "output_format")
	}
	if options.Speed != 0 && s.SpeedKey == "" {
		unsupported = append(unsupported, "speed")
	}
	if options.Voice != "" && s.VoiceKey == "" {
		unsupported = append(unsupported, "voice")
	}
	if len(unsupported) == 0 {
		return nil
	}
	return fmt.Errorf("replicate: speech: unsupported options: %s", strings.Join(unsupported, ", "))
}

// XTTSV2SpeechInputSchema returns the official schema binding for the pinned
// [ModelXTTSV2] version.
func XTTSV2SpeechInputSchema() SpeechInputSchema {
	return SpeechInputSchema{
		TextKey:       "text",
		VoiceKey:      "speaker",
		VoiceRequired: true,
		OutputKind:    FileOutputURI,
	}
}

var _ tts.Model = (*AudioTTSModel)(nil)

// AudioTTSModel wraps one explicitly bound Replicate TTS model version. It
// never infers fields from a model name: community models have independent,
// versioned schemas and must be constructed with the matching binding.
type AudioTTSModel struct {
	predictions    predictionRunner
	model          string
	inputSchema    SpeechInputSchema
	defaultOptions tts.Options
}

// NewAudioTTSModel rejects an invalid provider binding before the first speech call.
func NewAudioTTSModel(_ context.Context, config AudioTTSModelConfig) (*AudioTTSModel, error) {
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
	return &AudioTTSModel{
		predictions:    newPredictionRunner(api, config.PollInterval, config.PollTimeout, time.Duration(DefaultTTSPollTimeoutSeconds)*time.Second),
		model:          config.DefaultOptions.Model,
		inputSchema:    config.InputSchema,
		defaultOptions: config.DefaultOptions.Clone(),
	}, nil
}

func (a *AudioTTSModel) Call(ctx context.Context, req *tts.Request) (*tts.Response, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	effectiveOptions, apiRequest, err := a.prepareRequest(req)
	if err != nil {
		return nil, err
	}
	final, err := a.predictions.run(ctx, effectiveOptions.Model, apiRequest)
	if err != nil {
		return nil, err
	}
	return a.response(ctx, effectiveOptions, final)
}

func (a *AudioTTSModel) prepareRequest(req *tts.Request) (tts.Options, *predictionRequest, error) {
	effectiveOptions, err := a.defaultOptions.Resolve(req.Options)
	if err != nil {
		return tts.Options{}, nil, err
	}
	if effectiveOptions.Model != a.model {
		return tts.Options{}, nil, fmt.Errorf("replicate: speech: model override %q does not match bound schema for %q", effectiveOptions.Model, a.model)
	}
	if validateOptionsErr := a.inputSchema.validateOptions(effectiveOptions); validateOptionsErr != nil {
		return tts.Options{}, nil, validateOptionsErr
	}

	apiReqValue, _, err := effectiveOptions.Extensions.Decode[predictionRequest](SpeechRequestExtensionKey)
	if err != nil {
		return tts.Options{}, nil, err
	}
	apiReq := &apiReqValue
	if apiReq.Input == nil {
		apiReq.Input = map[string]any{}
	}

	apiReq.Input[a.inputSchema.TextKey] = req.Text
	if effectiveOptions.Voice != "" {
		apiReq.Input[a.inputSchema.VoiceKey] = effectiveOptions.Voice
	}
	if effectiveOptions.Speed > 0 {
		apiReq.Input[a.inputSchema.SpeedKey] = effectiveOptions.Speed
	}
	if a.inputSchema.VoiceRequired {
		voice, exists := apiReq.Input[a.inputSchema.VoiceKey]
		if !exists || voice == nil || voice == "" {
			return tts.Options{}, nil, fmt.Errorf("replicate: speech: model %q requires input %q", a.model, a.inputSchema.VoiceKey)
		}
	}
	return effectiveOptions, apiReq, nil
}

func (a *AudioTTSModel) response(ctx context.Context, effectiveOptions tts.Options, final *predictionResponse) (*tts.Response, error) {
	url, err := firstAudioURL(final.Output, a.inputSchema.OutputKind)
	if err != nil {
		return nil, err
	}

	audio, contentType, err := a.predictions.api.downloadOutput(ctx, url)
	if err != nil {
		return nil, err
	}

	var outputMetadata metadata.Map
	if contentType != "" {
		if setErr := outputMetadata.Set("replicate/mime_type", contentType); setErr != nil {
			return nil, setErr
		}
	}
	if milliseconds, present := final.predictTimeMilliseconds(); present {
		if setErr := outputMetadata.Set("replicate/predict_time_ms", milliseconds); setErr != nil {
			return nil, setErr
		}
	}

	output, err := tts.NewOutput(audio, outputMetadata)
	if err != nil {
		return nil, err
	}

	meta := &tts.ResponseMetadata{Model: effectiveOptions.Model}
	if final.CreatedAt != "" {
		createdAt, err := time.Parse(time.RFC3339Nano, final.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("replicate: invalid prediction created_at %q: %w", final.CreatedAt, err)
		}
		meta.CreatedAt = createdAt.UTC()
	}
	if err := meta.Extra.Set("replicate/prediction_id", final.ID); err != nil {
		return nil, err
	}
	if final.Version != "" {
		if err := meta.Extra.Set("replicate/version", final.Version); err != nil {
			return nil, err
		}
	}
	if err := meta.Extra.Set("replicate/audio_url", url); err != nil {
		return nil, err
	}
	if err := meta.Extra.Set(SpeechResponseExtensionKey, final); err != nil {
		return nil, err
	}
	return tts.NewResponse(output, meta)
}

func firstAudioURL(out any, kind FileOutputKind) (string, error) {
	if out == nil {
		return "", errors.New("replicate: speech output is null")
	}
	switch kind {
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
		return "", fmt.Errorf("replicate: unsupported speech output schema %q", kind)
	}
}
