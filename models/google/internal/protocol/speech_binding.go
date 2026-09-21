package protocol

import (
	"context"
	"errors"
	"slices"

	"google.golang.org/genai"

	"github.com/Tangerg/scope/core/metadata"
	tts "github.com/Tangerg/scope/core/speech"
)

type speechBinding struct {
	api            *api
	provider       string
	defaultOptions tts.Options
}

func newSpeechBinding(ctx context.Context, config AudioTTSModelConfig) (*speechBinding, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	api, err := newAPI(ctx, config.Client)
	if err != nil {
		return nil, err
	}

	return &speechBinding{
		api:            api,
		provider:       config.Provider,
		defaultOptions: config.DefaultOptions.Clone(),
	}, nil
}

func (s *speechBinding) buildAPITTSRequest(req *tts.Request) (string, []*genai.Content, *genai.GenerateContentConfig, error) {
	effectiveOptions, err := s.defaultOptions.Resolve(req.Options)
	if err != nil {
		return "", nil, nil, err
	}
	if validateOptionsErr := s.validateOptions(effectiveOptions); validateOptionsErr != nil {
		return "", nil, nil, validateOptionsErr
	}

	cfgValue, _, err := effectiveOptions.Extensions.Decode[genai.GenerateContentConfig](protocolKey(s.provider, "speech_request"))

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

	if config.SpeechConfig != nil && config.SpeechConfig.VoiceConfig != nil {
		return "", nil, nil, errors.New("google: speech: native voiceConfig is owned by Core Voice")
	}
	if effectiveOptions.Voice != "" {
		if config.SpeechConfig == nil {
			config.SpeechConfig = &genai.SpeechConfig{}
		}
		if config.SpeechConfig.MultiSpeakerVoiceConfig != nil {
			return "", nil, nil, errors.New("google: speech: Core Voice conflicts with multi-speaker configuration")
		}
		config.SpeechConfig.VoiceConfig = &genai.VoiceConfig{PrebuiltVoiceConfig: &genai.PrebuiltVoiceConfig{VoiceName: effectiveOptions.Voice}}
	}

	contents := []*genai.Content{
		genai.NewContentFromText(req.Text, genai.RoleUser),
	}

	return effectiveOptions.Model, contents, config, nil
}

func (*speechBinding) validateOptions(options tts.Options) error {
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

func (s *speechBinding) buildTTSResponse(apiResp *genai.GenerateContentResponse) (*tts.Response, error) {
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
		if err := outputMetadata.Set(protocolKey(s.provider, "mime_type"), mimeType); err != nil {
			return nil, err
		}
	}

	output, err := tts.NewOutput(audio, outputMetadata)
	if err != nil {
		return nil, err
	}

	meta := &tts.ResponseMetadata{Model: apiResp.ModelVersion}
	if err := meta.Extra.Set(protocolKey(s.provider, "speech_response"), apiResp); err != nil {
		return nil, err
	}

	return tts.NewResponse(output, meta)
}
