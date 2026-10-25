package mistral

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/Tangerg/scope/core/moderation"
)

type ModerationModelConfig struct {
	APIKey         string
	DefaultOptions moderation.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (m ModerationModelConfig) Validate() error {
	if m.APIKey == "" {
		return errors.New("mistral: API key is required")
	}
	if m.DefaultOptions.Model == "" {
		return errors.New("mistral: default model is required")
	}
	if err := m.DefaultOptions.Validate(); err != nil {
		return err
	}
	return nil
}

var _ moderation.Model = (*ModerationModel)(nil)

// ModerationModel wraps Mistral's /moderations endpoint. Mistral
// reports a custom category set (sexual / hate_and_discrimination /
// violence_and_threats / dangerous_and_criminal_content / selfharm /
// health / financial / law / pii). Category names are preserved exactly.
type ModerationModel struct {
	api            *api
	defaultOptions moderation.Options
}

func NewModerationModel(ctx context.Context, config ModerationModelConfig) (*ModerationModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	api, err := newAPI(ctx, apiConfig{APIKey: config.APIKey, BaseURL: config.BaseURL, HTTPClient: config.HTTPClient})
	if err != nil {
		return nil, err
	}
	return &ModerationModel{api: api, defaultOptions: config.DefaultOptions.Clone()}, nil
}

func (m *ModerationModel) Call(ctx context.Context, req *moderation.Request) (response *moderation.Response, err error) {
	if err = req.Validate(); err != nil {
		return nil, err
	}
	// Mistral answers with one result per input in submission order and tags
	// none of them, so counting the reply against the request is the only thing
	// that keeps each verdict attached to the text it judged.
	defer func() {
		if err == nil {
			err = response.ValidateFor(req)
		}
	}()

	effectiveOptions, err := m.defaultOptions.Resolve(req.Options)
	if err != nil {
		return nil, err
	}

	apiResp, err := m.api.moderation(ctx, &moderationRequest{
		Model: effectiveOptions.Model,
		Input: req.Texts,
	})
	if err != nil {
		return nil, err
	}

	outputs := make([]*moderation.Output, 0, len(apiResp.Results))
	for _, item := range apiResp.Results {
		cats, err := m.mapCategories(item.Categories, item.CategoryScores)
		if err != nil {
			return nil, err
		}
		res, err := moderation.NewOutput(cats, nil)
		if err != nil {
			return nil, err
		}
		outputs = append(outputs, res)
	}

	meta := &moderation.ResponseMetadata{
		ID:    apiResp.ID,
		Model: apiResp.Model,
	}
	return moderation.NewResponse(outputs, meta)
}

func (m *ModerationModel) mapCategories(flags map[string]*bool, scores map[string]*float64) (moderation.Categories, error) {
	if len(flags) != len(scores) {
		return nil, fmt.Errorf("%w: mistral: category flags and scores do not match", moderation.ErrInvalidResponse)
	}
	categories := make(moderation.Categories, len(flags))
	for category, flagged := range flags {
		score := scores[category]
		if flagged == nil || score == nil {
			return nil, fmt.Errorf("%w: mistral: category %q requires a flag and score", moderation.ErrInvalidResponse, category)
		}
		categories[category] = moderation.Verdict{Flagged: *flagged, Score: *score}
	}
	return categories, nil
}
