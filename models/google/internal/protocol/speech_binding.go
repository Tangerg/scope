package protocol

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"mime"
	"strings"

	"google.golang.org/genai"

	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/speech"
)

const speechMediaTypePrefix = "audio/"

type speechBinding struct {
	api            *api
	provider       string
	defaultOptions speech.Options
}

func newSpeechBinding(ctx context.Context, config SpeechModelConfig) (*speechBinding, error) {
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

func (s *speechBinding) buildAPITTSRequest(req *speech.Request) (string, []*genai.Content, *genai.GenerateContentConfig, error) {
	effectiveOptions, err := s.defaultOptions.Resolve(req.Options)
	if err != nil {
		return "", nil, nil, err
	}
	if validateOptionsErr := s.validateOptions(effectiveOptions); validateOptionsErr != nil {
		return "", nil, nil, validateOptionsErr
	}

	config, fields, err := decodeGenerationConfig(effectiveOptions.Extensions, protocolKey(s.provider, "speech_request"))
	if err != nil {
		return "", nil, nil, err
	}
	if _, found := fields["responseModalities"]; found {
		return "", nil, nil, errors.New("google: speech: responseModalities is owned by the audio capability")
	}
	if raw, found := fields["speechConfig"]; found {
		var speechFields map[string]json.RawMessage
		if err := jsonv2.Unmarshal(raw, &speechFields); err != nil {
			return "", nil, nil, err
		}
		if _, found := speechFields["voiceConfig"]; found {
			return "", nil, nil, errors.New("google: speech: native voiceConfig is owned by Core Voice")
		}
	}
	config.ResponseModalities = []string{string(genai.ModalityAudio)}
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

func (*speechBinding) validateOptions(options speech.Options) error {
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

func (s *speechBinding) buildTTSResponse(apiResp *genai.GenerateContentResponse, format *speechAudioFormat) (*speech.Response, error) {
	if apiResp == nil || len(apiResp.Candidates) == 0 {
		return nil, errNoAudio
	}

	if apiResp.Candidates[0] == nil {
		return nil, errors.New("google: speech: null candidate")
	}
	if apiResp.Candidates[0].Content == nil {
		return nil, errNoAudio
	}

	var audio []byte
	for _, part := range apiResp.Candidates[0].Content.Parts {
		if part == nil {
			return nil, errors.New("google: speech: null content part")
		}
		if part.InlineData == nil || len(part.InlineData.Data) == 0 {
			continue
		}
		if err := format.accept(part.InlineData.MIMEType); err != nil {
			return nil, err
		}
		audio = append(audio, part.InlineData.Data...)
	}
	if len(audio) == 0 {
		return nil, errNoAudio
	}

	var outputMetadata metadata.Map
	if err := outputMetadata.Set(protocolKey(s.provider, "mime_type"), string(*format)); err != nil {
		return nil, err
	}

	output, err := speech.NewOutput(audio, outputMetadata)
	if err != nil {
		return nil, err
	}

	meta := &speech.ResponseMetadata{Model: apiResp.ModelVersion}
	if err := meta.Extra.Set(protocolKey(s.provider, "speech_response"), apiResp); err != nil {
		return nil, err
	}

	return speech.NewResponse(output, meta)
}

// Concatenated audio must retain one encoding and sample rate across all parts
// and stream chunks; a new header cannot make earlier bytes use a new format.
type speechAudioFormat string

func (s *speechAudioFormat) accept(value string) error {
	mediaType, parameters, err := mime.ParseMediaType(value)
	if err != nil || !strings.HasPrefix(mediaType, speechMediaTypePrefix) {
		return fmt.Errorf("google: %w: invalid speech MIME type %q", speech.ErrInvalidResponse, value)
	}
	canonical := speechAudioFormat(mime.FormatMediaType(mediaType, parameters))
	if *s != "" && *s != canonical {
		return fmt.Errorf("google: %w: speech format changed from %q to %q", speech.ErrInvalidResponse, *s, canonical)
	}
	*s = canonical
	return nil
}
