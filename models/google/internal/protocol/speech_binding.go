package protocol

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"mime"

	"google.golang.org/genai"

	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/speech"
)

const (
	speechResponseModalitiesField = "responseModalities"
	speechConfigField             = "speechConfig"
	speechVoiceConfigField        = "voiceConfig"
)

type speechBinding struct {
	api              *api
	provider         string
	defaultOptions   speech.Options
	maxResponseBytes int64
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
		api:              api,
		provider:         config.Provider,
		defaultOptions:   config.DefaultOptions.Clone(),
		maxResponseBytes: config.MaxResponseBytes.Bytes(),
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

	config, fields, err := decodeSingleCandidateConfig(effectiveOptions.Extensions, protocolKey(s.provider, SpeechRequestExtensionSuffix))
	if err != nil {
		return "", nil, nil, err
	}
	if _, found := fields[speechResponseModalitiesField]; found {
		return "", nil, nil, fmt.Errorf("google: speech: %s belongs to the audio capability: %w", speechResponseModalitiesField, metadata.ErrCoreOwnedField)
	}
	if raw, found := fields[speechConfigField]; found {
		var speechFields map[string]json.RawMessage
		if err := jsonv2.Unmarshal(raw, &speechFields); err != nil {
			return "", nil, nil, err
		}
		if _, found := speechFields[speechVoiceConfigField]; found {
			return "", nil, nil, fmt.Errorf("google: speech: native %s: %w", speechVoiceConfigField, metadata.ErrCoreOwnedField)
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

func (s *speechBinding) buildTTSResponse(apiResp *genai.GenerateContentResponse, format *speechAudioFormat, maxAudioBytes int64) (*speech.Response, error) {
	if apiResp == nil || len(apiResp.Candidates) == 0 {
		return nil, errNoAudio
	}

	if apiResp.Candidates[0] == nil {
		return nil, errors.New("google: speech: null candidate")
	}
	if apiResp.Candidates[0].Content == nil {
		return nil, errNoAudio
	}

	response, err := speech.Collect(func(yield func(*speech.Response, error) bool) {
		received := false
		for index, part := range apiResp.Candidates[0].Content.Parts {
			if err := validateGenerationPart(part); err != nil {
				yield(nil, fmt.Errorf("google: speech: %w: content.parts[%d]: %w", speech.ErrInvalidResponse, index, err))
				return
			}
			if part.InlineData == nil || len(part.InlineData.Data) == 0 {
				continue
			}
			if err := format.accept(part.InlineData.MIMEType); err != nil {
				yield(nil, err)
				return
			}
			received = true
			if !yield(&speech.Response{Output: &speech.Output{MIME: string(*format), Audio: part.InlineData.Data}}, nil) {
				return
			}
		}
		if !received {
			yield(nil, errNoAudio)
		}
	}, maxAudioBytes)
	if err != nil {
		return nil, err
	}

	meta := &speech.ResponseMetadata{Model: apiResp.ModelVersion}
	if err := meta.Extra.Set(protocolKey(s.provider, SpeechResponseExtensionSuffix), apiResp); err != nil {
		return nil, err
	}

	return speech.NewResponse(response.Output, meta)
}

// Concatenated audio must retain one encoding and sample rate across all parts
// and stream chunks; a new header cannot make earlier bytes use a new format.
type speechAudioFormat string

func (s *speechAudioFormat) accept(value string) error {
	mediaType, parameters, err := mime.ParseMediaType(value)
	if err != nil || media.Type(mediaType).TopLevel() != media.TopLevelAudio {
		return fmt.Errorf("google: %w: invalid speech MIME type %q", speech.ErrInvalidResponse, value)
	}
	canonical := speechAudioFormat(mime.FormatMediaType(mediaType, parameters))
	if *s != "" && *s != canonical {
		return fmt.Errorf("google: %w: speech format changed from %q to %q", speech.ErrInvalidResponse, *s, canonical)
	}
	*s = canonical
	return nil
}
