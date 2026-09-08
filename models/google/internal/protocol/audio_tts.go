package protocol

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"slices"

	"google.golang.org/genai"

	"github.com/Tangerg/scope/core/metadata"
	tts "github.com/Tangerg/scope/core/speech"
)

// AudioTTSModelConfig binds provider access and defaults shared by every speech call.
type AudioTTSModelConfig struct {
	Provider       string
	Client         ClientConfig
	DefaultOptions tts.Options
}

func (a AudioTTSModelConfig) Validate() error {
	if err := validateProvider(a.Provider); err != nil {
		return fmt.Errorf("google: Provider: %w", err)
	}
	if err := a.Client.Validate(); err != nil {
		return err
	}
	if a.DefaultOptions.Model == "" {
		return errors.New("google: DefaultOptions.Model is required")
	}
	if err := a.DefaultOptions.Validate(); err != nil {
		return err
	}
	return nil
}

var _ tts.Model = (*AudioTTSModel)(nil)
var _ tts.Streamer = (*AudioTTSModel)(nil)

// AudioTTSModel wraps Gemini's native TTS through GenerateContent with
// ResponseModalities=AUDIO. Current supported models are declared in
// constant.go; only Gemini 3.1 Flash TTS supports incremental streaming.
//
// Speed and OutputFormat are not honored: Gemini's TTS has no
// playback-rate knob. GenerateContent returns 24 kHz signed 16-bit
// little-endian PCM; callers choose their own container at the application
// boundary.
type AudioTTSModel struct {
	api            *api
	provider       string
	defaultOptions tts.Options
}

// NewAudioTTSModel rejects an invalid provider binding before the first speech call.
func NewAudioTTSModel(ctx context.Context, config AudioTTSModelConfig) (*AudioTTSModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	api, err := newAPI(ctx, config.Client)
	if err != nil {
		return nil, err
	}

	return &AudioTTSModel{
		api:            api,
		provider:       config.Provider,
		defaultOptions: config.DefaultOptions.Clone(),
	}, nil
}

func (a *AudioTTSModel) buildAPITTSRequest(req *tts.Request) (string, []*genai.Content, *genai.GenerateContentConfig, error) {
	effectiveOptions, err := a.defaultOptions.Resolve(req.Options)
	if err != nil {
		return "", nil, nil, err
	}
	if validateOptionsErr := a.validateOptions(effectiveOptions); validateOptionsErr != nil {
		return "", nil, nil, validateOptionsErr
	}

	cfgValue, _, err := effectiveOptions.Extensions.Decode[genai.GenerateContentConfig](protocolKey(a.provider, "speech_request"))

	config := &cfgValue
	if err != nil {
		return "", nil, nil, err
	}

	// Force AUDIO output. The caller may have set ResponseModalities via
	// Extra (e.g. ["AUDIO", "TEXT"] for hybrid response); preserve that
	// when it already includes AUDIO, otherwise overwrite.
	if !slices.Contains(config.ResponseModalities, string(genai.ModalityAudio)) {
		config.ResponseModalities = []string{string(genai.ModalityAudio)}
	}

	// Voice routes onto SpeechConfig.VoiceConfig.PrebuiltVoiceConfig.VoiceName.
	// If the caller already threaded a richer SpeechConfig through Extra
	// (multi-speaker dialog, language code, replicated voice) it is
	// kept; the prebuilt-voice slot is only filled when the caller
	// did not supply one.
	if effectiveOptions.Voice != "" {
		if config.SpeechConfig == nil {
			config.SpeechConfig = &genai.SpeechConfig{}
		}
		if config.SpeechConfig.VoiceConfig == nil && config.SpeechConfig.MultiSpeakerVoiceConfig == nil {
			config.SpeechConfig.VoiceConfig = &genai.VoiceConfig{
				PrebuiltVoiceConfig: &genai.PrebuiltVoiceConfig{
					VoiceName: effectiveOptions.Voice,
				},
			}
		}
	}

	contents := []*genai.Content{
		genai.NewContentFromText(req.Text, genai.RoleUser),
	}

	return effectiveOptions.Model, contents, config, nil
}

func (*AudioTTSModel) validateOptions(options tts.Options) error {
	switch {
	case options.OutputFormat != "":
		return errors.New("google: speech: output_format is not supported")
	case options.Speed != 0:
		return errors.New("google: speech: speed is not supported")
	default:
		return nil
	}
}

// errNoAudio signals "this chunk contained no audio Parts". Returned by
// buildTTSResponse so the streaming loop can skip such chunks without
// terminating the whole stream.
var errNoAudio = errors.New("google: tts chunk has no audio inline-data parts")

func (a *AudioTTSModel) buildTTSResponse(apiResp *genai.GenerateContentResponse) (*tts.Response, error) {
	if len(apiResp.Candidates) == 0 || apiResp.Candidates[0].Content == nil {
		return nil, errNoAudio
	}

	// Capture mime type from the first audio-bearing Part — preceding
	// Parts may be thought / metadata with nil InlineData.
	var (
		audio    []byte
		mimeType string
	)
	for _, part := range apiResp.Candidates[0].Content.Parts {
		if part.InlineData == nil || len(part.InlineData.Data) == 0 {
			continue
		}
		if mimeType == "" {
			mimeType = part.InlineData.MIMEType
		}
		audio = append(audio, part.InlineData.Data...)
	}
	if len(audio) == 0 {
		return nil, errNoAudio
	}

	var outputMetadata metadata.Map
	if mimeType != "" {
		if err := outputMetadata.Set(protocolKey(a.provider, "mime_type"), mimeType); err != nil {
			return nil, err
		}
	}

	output, err := tts.NewOutput(audio, outputMetadata)
	if err != nil {
		return nil, err
	}

	meta := &tts.ResponseMetadata{Model: apiResp.ModelVersion}
	if err := meta.Extra.Set(protocolKey(a.provider, "speech_response"), apiResp); err != nil {
		return nil, err
	}

	return tts.NewResponse(output, meta)
}

func (a *AudioTTSModel) Call(ctx context.Context, req *tts.Request) (*tts.Response, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	modelName, contents, config, err := a.buildAPITTSRequest(req)
	if err != nil {
		return nil, err
	}

	apiResp, err := a.api.chatCompletion(ctx, modelName, contents, config)
	if err != nil {
		return nil, err
	}

	return a.buildTTSResponse(apiResp)
}

func (a *AudioTTSModel) Stream(ctx context.Context, req *tts.Request) iter.Seq2[*tts.Response, error] {
	return func(yield func(*tts.Response, error) bool) {
		if err := req.Validate(); err != nil {
			yield(nil, err)
			return
		}
		modelName, contents, config, err := a.buildAPITTSRequest(req)
		if err != nil {
			yield(nil, err)
			return
		}
		if modelName != ModelGemini31FlashTTSPreview {
			yield(nil, fmt.Errorf("google: speech: model %q does not support streaming; use %q", modelName, ModelGemini31FlashTTSPreview))
			return
		}

		for chunk, err := range a.api.chatCompletionStream(ctx, modelName, contents, config) {
			if err != nil {
				yield(nil, err)
				return
			}

			resp, err := a.buildTTSResponse(chunk)
			if err != nil {
				// Skip chunks that don't carry audio (Gemini may emit
				// metadata-only chunks during streaming) rather than
				// fail the whole stream.
				if errors.Is(err, errNoAudio) {
					continue
				}
				yield(nil, err)
				return
			}
			if !yield(resp, nil) {
				return
			}
		}
	}
}
