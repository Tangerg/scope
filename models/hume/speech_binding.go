package hume

import (
	"context"
	"errors"
	"fmt"

	tts "github.com/Tangerg/scope/core/speech"
)

type speechBinding struct {
	api            *api
	defaultOptions tts.Options
}

func newSpeechBinding(_ context.Context, config AudioTTSModelConfig) (*speechBinding, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	api, err := newAPI(apiConfig{APIKey: config.APIKey, BaseURL: config.BaseURL, HTTPClient: config.HTTPClient})
	if err != nil {
		return nil, err
	}
	return &speechBinding{api: api, defaultOptions: config.DefaultOptions.Clone()}, nil
}

func (s *speechBinding) buildAPIRequest(req *tts.Request) (*ttsRequest, error) {
	effectiveOptions, err := s.defaultOptions.Resolve(req.Options)
	if err != nil {
		return nil, err
	}

	nativeFields, _, err := effectiveOptions.Extensions.Decode[map[string]any](SpeechRequestExtensionKey)
	if err != nil {
		return nil, err
	}
	if _, exists := nativeFields["version"]; exists {
		return nil, fmt.Errorf("hume: extension %q version is owned by Core", SpeechRequestExtensionKey)
	}
	if format, ok := nativeFields["format"].(map[string]any); ok {
		if _, exists := format["type"]; exists {
			return nil, fmt.Errorf("hume: extension %q format.type is owned by Core", SpeechRequestExtensionKey)
		}
		if len(format) > 0 && effectiveOptions.OutputFormat == "" {
			return nil, errors.New("hume: native format options require Options.OutputFormat")
		}
	}
	if utterances, ok := nativeFields["utterances"].([]any); ok {
		if len(utterances) > 1 {
			return nil, errors.New("hume: multiple utterances cannot be represented by Core's single-text request")
		}
		for _, value := range utterances {
			utterance, ok := value.(map[string]any)
			if !ok {
				return nil, errors.New("hume: utterance must be an object")
			}
			for _, field := range []string{"text", "speed"} {
				if _, exists := utterance[field]; exists {
					return nil, fmt.Errorf("hume: extension utterance %s is owned by Core", field)
				}
			}
			if voice, ok := utterance["voice"].(map[string]any); ok {
				for _, field := range []string{"id", "name"} {
					if _, exists := voice[field]; exists {
						return nil, fmt.Errorf("hume: extension utterance voice.%s is owned by Core", field)
					}
				}
				if effectiveOptions.Voice == "" {
					return nil, errors.New("hume: native voice options require Options.Voice")
				}
			}
		}
	}

	bodyValue, _, err := effectiveOptions.Extensions.Decode[ttsRequest](SpeechRequestExtensionKey)

	body := &bodyValue
	if err != nil {
		return nil, err
	}
	if effectiveOptions.Model != ModelOctave1 && effectiveOptions.Model != ModelOctave2 {
		return nil, fmt.Errorf("hume: speech: model must be %q or %q", ModelOctave1, ModelOctave2)
	}
	if body.NumGenerations > 1 {
		return nil, errors.New("hume: speech: num_generations greater than 1 cannot be represented by Core's single-output response")
	}
	if len(body.Utterances) == 0 {
		body.Utterances = []utterance{{}}
	}
	body.Utterances[0].Text = req.Text
	if effectiveOptions.Voice != "" {
		if body.Utterances[0].Voice == nil {
			body.Utterances[0].Voice = &voice{}
		}
		body.Utterances[0].Voice.ID = effectiveOptions.Voice
		if body.Utterances[0].Voice.Provider == "" {
			body.Utterances[0].Voice.Provider = "HUME_AI"
		}
	}
	if effectiveOptions.Speed != 0 {
		v := effectiveOptions.Speed
		body.Utterances[0].Speed = &v
	}
	body.Version = effectiveOptions.Model
	if effectiveOptions.OutputFormat != "" {
		switch effectiveOptions.OutputFormat {
		case "mp3", "wav", "pcm":
		default:
			return nil, errors.New("hume: speech: output_format must be mp3, wav, or pcm")
		}
		if body.Format == nil {
			body.Format = map[string]any{}
		}
		body.Format["type"] = effectiveOptions.OutputFormat
	}
	if body.Version == ModelOctave2 && body.Utterances[0].Voice == nil {
		return nil, errors.New("hume: speech: Octave 2 requires Options.Voice")
	}
	return body, nil
}
