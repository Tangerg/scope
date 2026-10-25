package openai

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/openai/openai-go/v3"

	"github.com/Tangerg/scope/core/moderation"
)

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
	if err := (apiConfig{APIKey: m.APIKey, BaseURL: m.BaseURL}).validate(); err != nil {
		return err
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

func NewModerationModel(ctx context.Context, config ModerationModelConfig) (*ModerationModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	api, err := newAPI(ctx, apiConfig{
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
	if resp == nil {
		return nil, fmt.Errorf("%w: openai: moderation response is null", moderation.ErrInvalidResponse)
	}
	outputs := make([]*moderation.Output, 0, len(resp.Results))

	for _, item := range resp.Results {
		if !item.JSON.Categories.Valid() || !item.JSON.CategoryScores.Valid() {
			return nil, fmt.Errorf("%w: openai: moderation requires categories and category_scores", moderation.ErrInvalidResponse)
		}
		var flags map[string]*bool
		if err := jsonv2.Unmarshal([]byte(item.JSON.Categories.Raw()), &flags); err != nil {
			return nil, fmt.Errorf("%w: openai: decode moderation categories: %w", moderation.ErrInvalidResponse, err)
		}
		var scores map[string]*float64
		if err := jsonv2.Unmarshal([]byte(item.JSON.CategoryScores.Raw()), &scores); err != nil {
			return nil, fmt.Errorf("%w: openai: decode moderation scores: %w", moderation.ErrInvalidResponse, err)
		}
		if len(flags) != len(scores) {
			return nil, fmt.Errorf("%w: openai: category flags and scores do not match", moderation.ErrInvalidResponse)
		}
		cats := make(moderation.Categories, len(flags))
		for category, flagged := range flags {
			score := scores[category]
			if flagged == nil || score == nil {
				return nil, fmt.Errorf("%w: openai: category %q requires a flag and score", moderation.ErrInvalidResponse, category)
			}
			name := strings.ReplaceAll(strings.ReplaceAll(category, "/", "_"), "-", "_")
			if _, exists := cats[name]; exists {
				return nil, fmt.Errorf("%w: openai: category %q collides after normalization", moderation.ErrInvalidResponse, category)
			}
			cats[name] = moderation.Verdict{Flagged: *flagged, Score: *score}
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
