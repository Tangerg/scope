package hume

import (
	"cmp"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/speech"
)

const (
	requestFieldVersion    = "version"
	requestFieldFormat     = "format"
	formatFieldType        = "type"
	requestFieldUtterances = "utterances"
	utteranceFieldText     = "text"
	utteranceFieldSpeed    = "speed"
	utteranceFieldVoice    = "voice"
	voiceFieldID           = "id"
	voiceFieldName         = "name"
	voiceProviderHume      = "HUME_AI"
)

type speechBinding struct {
	api            *api
	defaultOptions speech.Options
}

func newSpeechBinding(ctx context.Context, config SpeechModelConfig) (*speechBinding, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	api, err := newAPI(ctx, config.apiConfig())
	if err != nil {
		return nil, err
	}
	return &speechBinding{api: api, defaultOptions: config.DefaultOptions.Clone()}, nil
}

func (s *speechBinding) buildAPIRequest(req *speech.Request) (*ttsRequest, error) {
	options, err := s.defaultOptions.Resolve(req.Options)
	if err != nil {
		return nil, err
	}
	body, err := mapSpeechOptions(options)
	if err != nil {
		return nil, err
	}
	first := &body.Utterances[0]
	first.Text = req.Text
	// Octave 2 needs a voice, but a request may supply it, so only the
	// effective request has to carry one.
	if body.Version == ModelOctave2 && first.Voice == nil {
		return nil, errors.New("hume: speech: Octave 2 requires Options.Voice")
	}
	return body, nil
}

// mapSpeechOptions projects the options alone onto the native request. A
// binding applies it to its defaults at construction and to every resolved
// request, so defaults no request could send fail when they are configured.
func mapSpeechOptions(options speech.Options) (*ttsRequest, error) {
	nativeFields, _, err := options.Extensions.Decode[map[string]any](SpeechRequestExtensionKey)
	if err != nil {
		return nil, err
	}
	if err = admitSpeechExtension(nativeFields, options); err != nil {
		return nil, err
	}
	body, _, err := options.Extensions.Decode[ttsRequest](SpeechRequestExtensionKey)
	if err != nil {
		return nil, err
	}
	if err = projectSpeech(&body, options); err != nil {
		return nil, err
	}
	return &body, nil
}

// admitSpeechExtension rejects native fields Core owns, and native voice or
// format options that would refine a Core value the request does not set.
func admitSpeechExtension(fields map[string]any, options speech.Options) error {
	if _, exists := fields[requestFieldVersion]; exists {
		return fmt.Errorf("hume: extension %q version: %w", SpeechRequestExtensionKey, metadata.ErrCoreOwnedField)
	}
	if format, ok := fields[requestFieldFormat].(map[string]any); ok {
		if _, exists := format[formatFieldType]; exists {
			return fmt.Errorf("hume: extension %q format.type: %w", SpeechRequestExtensionKey, metadata.ErrCoreOwnedField)
		}
		if len(format) > 0 && options.OutputFormat == "" {
			return errors.New("hume: native format options require Options.OutputFormat")
		}
	}
	utterances, ok := fields[requestFieldUtterances].([]any)
	if !ok {
		return nil
	}
	if len(utterances) > 1 {
		return errors.New("hume: multiple utterances cannot be represented by Core's single-text request")
	}
	for _, value := range utterances {
		if err := admitUtterance(value, options); err != nil {
			return err
		}
	}
	return nil
}

func admitUtterance(value any, options speech.Options) error {
	fields, ok := value.(map[string]any)
	if !ok {
		return errors.New("hume: utterance must be an object")
	}
	for _, field := range []string{utteranceFieldText, utteranceFieldSpeed} {
		if _, exists := fields[field]; exists {
			return fmt.Errorf("hume: extension utterance %s: %w", field, metadata.ErrCoreOwnedField)
		}
	}
	voiceFields, ok := fields[utteranceFieldVoice].(map[string]any)
	if !ok {
		return nil
	}
	for _, field := range []string{voiceFieldID, voiceFieldName} {
		if _, exists := voiceFields[field]; exists {
			return fmt.Errorf("hume: extension utterance voice.%s: %w", field, metadata.ErrCoreOwnedField)
		}
	}
	if options.Voice == "" {
		return errors.New("hume: native voice options require Options.Voice")
	}
	return nil
}

// projectSpeech writes Core's text, model, voice, speed and output format
// into the single native utterance request.
func projectSpeech(body *ttsRequest, options speech.Options) error {
	if options.Model != ModelOctave1 && options.Model != ModelOctave2 {
		return fmt.Errorf("hume: speech: model must be %q or %q", ModelOctave1, ModelOctave2)
	}
	if body.NumGenerations != nil && *body.NumGenerations != 1 {
		return errors.New("hume: speech: num_generations must be 1 for Core's single-output response")
	}
	if len(body.Utterances) == 0 {
		body.Utterances = []utterance{{}}
	}
	first := &body.Utterances[0]
	if options.Voice != "" {
		if first.Voice == nil {
			first.Voice = &voice{}
		}
		first.Voice.ID = options.Voice
		first.Voice.Provider = cmp.Or(first.Voice.Provider, voiceProviderHume)
	}
	if options.Speed != 0 {
		first.Speed = new(options.Speed)
	}
	body.Version = options.Model
	return projectFormat(body, options.OutputFormat)
}

func projectFormat(body *ttsRequest, format string) error {
	if format == "" {
		return nil
	}
	switch format {
	case audioFormatMP3, audioFormatWAV, audioFormatPCM:
	default:
		return errors.New("hume: speech: output_format must be mp3, wav, or pcm")
	}
	if body.Format == nil {
		body.Format = map[string]json.RawMessage{}
	}
	// Encode the Core-owned format type as its own JSON token so the native
	// format subtree stays a lossless representation alongside caller values.
	encoded, err := jsonv2.Marshal(format)
	if err != nil {
		return fmt.Errorf("hume: speech: encode output_format: %w", err)
	}
	body.Format[formatFieldType] = encoded
	return nil
}
