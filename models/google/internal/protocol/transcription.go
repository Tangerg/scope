package protocol

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/genai"

	"github.com/Tangerg/scope/core/transcription"
)

type TranscriptionModelConfig struct {
	Provider       string
	Client         ClientConfig
	DefaultOptions transcription.Options
}

func (t TranscriptionModelConfig) Validate() error {
	if err := validateProvider(t.Provider); err != nil {
		return fmt.Errorf("google: Provider: %w", err)
	}
	if err := t.Client.Validate(); err != nil {
		return err
	}
	if t.DefaultOptions.Model == "" {
		return errors.New("google: DefaultOptions.Model is required")
	}
	if err := t.DefaultOptions.Validate(); err != nil {
		return err
	}
	return nil
}

var _ transcription.Model = (*TranscriptionModel)(nil)

// TranscriptionModel exposes Gemini's multimodal chat through the
// transcription interface. Gemini has no /transcribe endpoint — any
// audio-accepting model returns a transcript when prompted. This adapter uses
// the stable instruction "Transcribe this audio.".
type TranscriptionModel struct {
	api            *api
	provider       string
	defaultOptions transcription.Options
}

func NewTranscriptionModel(ctx context.Context, config TranscriptionModelConfig) (*TranscriptionModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	api, err := newAPI(ctx, config.Client)
	if err != nil {
		return nil, err
	}

	return &TranscriptionModel{
		api:            api,
		provider:       config.Provider,
		defaultOptions: config.DefaultOptions.Clone(),
	}, nil
}

func (t *TranscriptionModel) buildAPITranscriptionRequest(req *transcription.Request) (string, []*genai.Content, *genai.GenerateContentConfig, error) {
	effectiveOptions, err := t.defaultOptions.Resolve(req.Options)
	if err != nil {
		return "", nil, nil, err
	}
	if validateOptionsErr := t.validateOptions(effectiveOptions); validateOptionsErr != nil {
		return "", nil, nil, validateOptionsErr
	}

	cfgValue, _, err := effectiveOptions.Extensions.Decode[genai.GenerateContentConfig](protocolKey(t.provider, "transcription_request"))

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

func (*TranscriptionModel) validateOptions(options transcription.Options) error {
	if options.Language != "" {
		return errors.New("google: transcription: language is not supported")
	}
	return nil
}

func (t *TranscriptionModel) buildTranscriptionResponse(apiResp *genai.GenerateContentResponse) (*transcription.Response, error) {
	if err := validateProtocolCompletion(apiResp); err != nil {
		return nil, fmt.Errorf("google: transcription: %w: %w", transcription.ErrInvalidResponse, err)
	}
	if apiResp.Candidates[0].Content == nil {
		return nil, fmt.Errorf("google: transcription: %w: candidate has no content", transcription.ErrInvalidResponse)
	}

	if apiResp.SDKHTTPResponse == nil {
		return nil, fmt.Errorf("%w: google: transcription has no native response", transcription.ErrInvalidResponse)
	}
	var native struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text    json.RawMessage `json:"text"`
					Thought bool            `json:"thought"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := jsonv2.Unmarshal([]byte(apiResp.SDKHTTPResponse.Body), &native); err != nil {
		return nil, fmt.Errorf("%w: google: transcription native response: %w", transcription.ErrInvalidResponse, err)
	}
	var text strings.Builder
	reported := false
	for _, candidate := range native.Candidates {
		for _, part := range candidate.Content.Parts {
			if part.Thought || len(part.Text) == 0 {
				continue
			}
			var content *string
			if err := jsonv2.Unmarshal(part.Text, &content); err != nil {
				return nil, fmt.Errorf("%w: google: transcript text: %w", transcription.ErrInvalidResponse, err)
			}
			if content == nil {
				return nil, fmt.Errorf("%w: google: transcript text must not be null", transcription.ErrInvalidResponse)
			}
			reported = true
			text.WriteString(*content)
		}
	}
	if !reported {
		return nil, fmt.Errorf("%w: google: response contains no transcript", transcription.ErrInvalidResponse)
	}

	output, err := transcription.NewOutput(text.String(), nil)
	if err != nil {
		return nil, err
	}

	meta := &transcription.ResponseMetadata{Model: apiResp.ModelVersion}
	if err := meta.Extra.Set(protocolKey(t.provider, "transcription_response"), apiResp); err != nil {
		return nil, err
	}

	return transcription.NewResponse(output, meta)
}

func (t *TranscriptionModel) Call(ctx context.Context, req *transcription.Request) (*transcription.Response, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	modelName, contents, config, err := t.buildAPITranscriptionRequest(req)
	if err != nil {
		return nil, err
	}

	apiResp, err := t.api.chatCompletion(ctx, modelName, contents, config)
	if err != nil {
		return nil, err
	}

	return t.buildTranscriptionResponse(apiResp)
}
