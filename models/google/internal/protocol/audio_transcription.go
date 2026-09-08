package protocol

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/genai"

	"github.com/Tangerg/scope/core/transcription"
)

// AudioTranscriptionModelConfig binds provider access and defaults shared by every transcription call.
type AudioTranscriptionModelConfig struct {
	Provider       string
	Client         ClientConfig
	DefaultOptions transcription.Options
}

func (a AudioTranscriptionModelConfig) Validate() error {
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

var _ transcription.Model = (*AudioTranscriptionModel)(nil)

// AudioTranscriptionModel exposes Gemini's multimodal chat through the
// transcription interface. Gemini has no /transcribe endpoint — any
// audio-accepting model returns a transcript when prompted. This adapter uses
// the stable instruction "Transcribe this audio.".
type AudioTranscriptionModel struct {
	api            *api
	provider       string
	defaultOptions transcription.Options
}

// NewAudioTranscriptionModel rejects an invalid provider binding before the first transcription call.
func NewAudioTranscriptionModel(ctx context.Context, config AudioTranscriptionModelConfig) (*AudioTranscriptionModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	api, err := newAPI(ctx, config.Client)
	if err != nil {
		return nil, err
	}

	return &AudioTranscriptionModel{
		api:            api,
		provider:       config.Provider,
		defaultOptions: config.DefaultOptions.Clone(),
	}, nil
}

func (a *AudioTranscriptionModel) buildAPITranscriptionRequest(req *transcription.Request) (string, []*genai.Content, *genai.GenerateContentConfig, error) {
	effectiveOptions, err := a.defaultOptions.Resolve(req.Options)
	if err != nil {
		return "", nil, nil, err
	}
	if validateOptionsErr := a.validateOptions(effectiveOptions); validateOptionsErr != nil {
		return "", nil, nil, validateOptionsErr
	}

	cfgValue, _, err := effectiveOptions.Extensions.Decode[genai.GenerateContentConfig](protocolKey(a.provider, "transcription_request"))

	config := &cfgValue
	if err != nil {
		return "", nil, nil, err
	}

	data, err := req.Audio.Bytes()
	if err != nil {
		return "", nil, nil, err
	}

	parts := []*genai.Part{
		genai.NewPartFromText("Transcribe this audio."),
		genai.NewPartFromBytes(data, req.Audio.MIME),
	}
	contents := []*genai.Content{
		genai.NewContentFromParts(parts, genai.RoleUser),
	}

	return effectiveOptions.Model, contents, config, nil
}

func (*AudioTranscriptionModel) validateOptions(options transcription.Options) error {
	if options.Language != "" {
		return errors.New("google: transcription: language is not supported")
	}
	return nil
}

func (a *AudioTranscriptionModel) buildTranscriptionResponse(apiResp *genai.GenerateContentResponse) (*transcription.Response, error) {
	if len(apiResp.Candidates) == 0 || apiResp.Candidates[0].Content == nil {
		return nil, errors.New("google: transcription response has no candidates")
	}

	var text strings.Builder
	for _, part := range apiResp.Candidates[0].Content.Parts {
		// Skip thought-flagged parts: Gemini 2.5 may emit reasoning
		// before producing the transcript; reasoning text must not be
		// pasted into the output.
		if part.Thought {
			continue
		}
		if part.Text != "" {
			text.WriteString(part.Text)
		}
	}

	output, err := transcription.NewOutput(text.String(), nil)
	if err != nil {
		return nil, err
	}

	meta := &transcription.ResponseMetadata{Model: apiResp.ModelVersion}
	if err := meta.Extra.Set(protocolKey(a.provider, "transcription_response"), apiResp); err != nil {
		return nil, err
	}

	return transcription.NewResponse(output, meta)
}

func (a *AudioTranscriptionModel) Call(ctx context.Context, req *transcription.Request) (*transcription.Response, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	modelName, contents, config, err := a.buildAPITranscriptionRequest(req)
	if err != nil {
		return nil, err
	}

	apiResp, err := a.api.chatCompletion(ctx, modelName, contents, config)
	if err != nil {
		return nil, err
	}

	return a.buildTranscriptionResponse(apiResp)
}
