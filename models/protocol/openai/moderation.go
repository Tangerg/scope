package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/openai/openai-go/v3"

	"github.com/Tangerg/scope/core/moderation"
)

// ModerationModelConfig binds provider access and defaults shared by every moderation call.
type ModerationModelConfig struct {
	Provider       string
	APIKey         string
	DefaultOptions moderation.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (m ModerationModelConfig) Validate() error {
	if err := validateProvider(m.Provider); err != nil {
		return fmt.Errorf("openai: Provider: %w", err)
	}
	if m.APIKey == "" {
		return errors.New("openai: APIKey is required")
	}
	if m.DefaultOptions.Model == "" {
		return errors.New("openai: DefaultOptions.Model is required")
	}
	if err := m.DefaultOptions.Validate(); err != nil {
		return err
	}
	return nil
}

var _ moderation.Model = (*ModerationModel)(nil)

// ModerationModel implements the OpenAI-compatible moderation protocol.
type ModerationModel struct {
	api            *api
	provider       string
	defaultOptions moderation.Options
}

// NewModerationModel rejects an invalid provider binding before the first moderation call.
func NewModerationModel(_ context.Context, config ModerationModelConfig) (*ModerationModel, error) {
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

	return &ModerationModel{
		api:            api,
		provider:       config.Provider,
		defaultOptions: config.DefaultOptions.Clone(),
	}, nil
}

func (m *ModerationModel) buildAPIModerationRequest(req *moderation.Request) (*openai.ModerationNewParams, error) {
	effectiveOptions, err := m.defaultOptions.Resolve(req.Options)
	if err != nil {
		return nil, err
	}

	fields, err := decodeRequestFields(effectiveOptions.Extensions, protocolModalityRequestExtensionKey(m.provider, "moderation"), "model", "input")
	if err != nil {
		return nil, err
	}
	params := &openai.ModerationNewParams{}
	params.SetExtraFields(fields)

	params.Model = effectiveOptions.Model
	params.Input = openai.ModerationNewParamsInputUnion{
		OfStringArray: req.Texts,
	}

	return params, nil
}

func (m *ModerationModel) buildModerationResponse(resp *openai.ModerationNewResponse) (*moderation.Response, error) {
	outputs := make([]*moderation.Output, 0, len(resp.Results))

	for _, item := range resp.Results {
		cats := moderation.Categories{
			"harassment": {
				Flagged: item.Categories.Harassment,
				Score:   item.CategoryScores.Harassment,
			},
			"harassment_threatening": {
				Flagged: item.Categories.HarassmentThreatening,
				Score:   item.CategoryScores.HarassmentThreatening,
			},
			"hate": {
				Flagged: item.Categories.Hate,
				Score:   item.CategoryScores.Hate,
			},
			"hate_threatening": {
				Flagged: item.Categories.HateThreatening,
				Score:   item.CategoryScores.HateThreatening,
			},
			"illicit": {
				Flagged: item.Categories.Illicit,
				Score:   item.CategoryScores.Illicit,
			},
			"illicit_violent": {
				Flagged: item.Categories.IllicitViolent,
				Score:   item.CategoryScores.IllicitViolent,
			},
			"self_harm": {
				Flagged: item.Categories.SelfHarm,
				Score:   item.CategoryScores.SelfHarm,
			},
			"self_harm_instructions": {
				Flagged: item.Categories.SelfHarmInstructions,
				Score:   item.CategoryScores.SelfHarmInstructions,
			},
			"self_harm_intent": {
				Flagged: item.Categories.SelfHarmIntent,
				Score:   item.CategoryScores.SelfHarmIntent,
			},
			"sexual": {
				Flagged: item.Categories.Sexual,
				Score:   item.CategoryScores.Sexual,
			},
			"sexual_minors": {
				Flagged: item.Categories.SexualMinors,
				Score:   item.CategoryScores.SexualMinors,
			},
			"violence": {
				Flagged: item.Categories.Violence,
				Score:   item.CategoryScores.Violence,
			},
			"violence_graphic": {
				Flagged: item.Categories.ViolenceGraphic,
				Score:   item.CategoryScores.ViolenceGraphic,
			},
		}

		output, err := moderation.NewOutput(cats, nil)
		if err != nil {
			return nil, err
		}

		outputs = append(outputs, output)
	}

	meta := &moderation.ResponseMetadata{
		ID:    resp.ID,
		Model: resp.Model,
	}

	return moderation.NewResponse(outputs, meta)
}

func (m *ModerationModel) Call(ctx context.Context, req *moderation.Request) (response *moderation.Response, err error) {
	if err = req.Validate(); err != nil {
		return nil, err
	}
	// The moderation results carry no index, so their order is the only thing
	// tying a verdict to an input. Counting the reply against the request is
	// what keeps each verdict attached to the text it judged.
	defer func() {
		if err == nil {
			err = response.ValidateFor(req)
		}
	}()

	apiReq, err := m.buildAPIModerationRequest(req)
	if err != nil {
		return nil, err
	}

	apiResp, err := m.api.moderation(ctx, apiReq)
	if err != nil {
		return nil, err
	}

	return m.buildModerationResponse(apiResp)
}
