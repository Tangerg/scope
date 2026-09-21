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
		body.Utterances[0].Voice = &voice{ID: effectiveOptions.Voice, Provider: "HUME_AI"}
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
		body.Format = map[string]any{"type": effectiveOptions.OutputFormat}
	}
	if body.Version == ModelOctave2 && body.Utterances[0].Voice == nil {
		return nil, errors.New("hume: speech: Octave 2 requires Options.Voice or a voice on the first utterance")
	}
	return body, nil
}
