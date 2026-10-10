package chat

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"slices"
	"unicode/utf8"

	"github.com/Tangerg/scope/core/internal/modelid"
	"github.com/Tangerg/scope/core/internal/ptr"
	"github.com/Tangerg/scope/core/metadata"
)

var ErrInvalidOptions = errors.New("chat: invalid options")

const (
	minimumPenalty     = -2.0
	maximumPenalty     = 2.0
	minimumTemperature = 0.0
	maximumTemperature = 2.0
	minimumTopP        = 0.0
	maximumTopP        = 1.0
)

// Options contains per-request overrides; zero values use provider defaults.
// Adapters must report unsupported populated fields, never drop them.
type Options struct {
	Model            string              `json:"model,omitempty"`
	OutputFormat     *OutputFormat       `json:"output_format,omitzero"`
	FrequencyPenalty *float64            `json:"frequency_penalty,omitzero"`
	MaxOutputTokens  *int64              `json:"max_output_tokens,omitzero"`
	PresencePenalty  *float64            `json:"presence_penalty,omitzero"`
	ReasoningEffort  ReasoningEffort     `json:"reasoning_effort,omitempty"`
	Stop             []string            `json:"stop,omitzero"`
	Temperature      *float64            `json:"temperature,omitzero"`
	TopK             *int64              `json:"top_k,omitzero"`
	TopP             *float64            `json:"top_p,omitzero"`
	Extensions       metadata.Extensions `json:"extensions,omitzero"`
}

func (o Options) Clone() Options {
	return Options{
		Model:            o.Model,
		OutputFormat:     o.OutputFormat.Clone(),
		FrequencyPenalty: ptr.Clone(o.FrequencyPenalty),
		MaxOutputTokens:  ptr.Clone(o.MaxOutputTokens),
		PresencePenalty:  ptr.Clone(o.PresencePenalty),
		ReasoningEffort:  o.ReasoningEffort,
		Stop:             slices.Clone(o.Stop),
		Temperature:      ptr.Clone(o.Temperature),
		TopK:             ptr.Clone(o.TopK),
		TopP:             ptr.Clone(o.TopP),
		Extensions:       o.Extensions.Clone(),
	}
}

// Resolve overlays only explicitly populated override values and returns an
// independently owned, validated result. Neither input is mutated.
func (o Options) Resolve(override Options) (Options, error) {
	effective := o.Clone()
	if err := effective.applyOverride(override); err != nil {
		return Options{}, fmt.Errorf("chat: resolve options: %w: %w", ErrInvalidOptions, err)
	}
	if err := effective.Validate(); err != nil {
		return Options{}, fmt.Errorf("chat: resolve options: %w", err)
	}
	return effective, nil
}

func (o *Options) applyOverride(override Options) error {
	if override.Model != "" {
		o.Model = override.Model
	}
	if override.OutputFormat != nil {
		o.OutputFormat = override.OutputFormat.Clone()
	}
	if override.FrequencyPenalty != nil {
		o.FrequencyPenalty = ptr.Clone(override.FrequencyPenalty)
	}
	if override.MaxOutputTokens != nil {
		o.MaxOutputTokens = ptr.Clone(override.MaxOutputTokens)
	}
	if override.PresencePenalty != nil {
		o.PresencePenalty = ptr.Clone(override.PresencePenalty)
	}
	if override.ReasoningEffort != "" {
		o.ReasoningEffort = override.ReasoningEffort
	}
	if override.Stop != nil {
		o.Stop = slices.Clone(override.Stop)
	}
	if override.Temperature != nil {
		o.Temperature = ptr.Clone(override.Temperature)
	}
	if override.TopK != nil {
		o.TopK = ptr.Clone(override.TopK)
	}
	if override.TopP != nil {
		o.TopP = ptr.Clone(override.TopP)
	}
	if !override.Extensions.IsZero() {
		if err := o.Extensions.Merge(override.Extensions); err != nil {
			return fmt.Errorf("merge extensions: %w", err)
		}
	}
	return nil
}

func (o Options) Validate() error {
	if err := modelid.Validate(o.Model); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidOptions, err)
	}
	if o.OutputFormat != nil {
		if err := o.OutputFormat.Validate(); err != nil {
			return fmt.Errorf("%w: output_format: %w", ErrInvalidOptions, err)
		}
	}
	if err := validateFloat("frequency_penalty", o.FrequencyPenalty, minimumPenalty, maximumPenalty); err != nil {
		return err
	}
	if err := validatePositive("max_output_tokens", o.MaxOutputTokens); err != nil {
		return err
	}
	if err := validateFloat("presence_penalty", o.PresencePenalty, minimumPenalty, maximumPenalty); err != nil {
		return err
	}
	if err := o.ReasoningEffort.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidOptions, err)
	}
	for i, stop := range o.Stop {
		if !utf8.ValidString(stop) {
			return fmt.Errorf("%w: stop[%d] must be valid UTF-8", ErrInvalidOptions, i)
		}
		if stop == "" {
			return fmt.Errorf("%w: stop[%d] must not be empty", ErrInvalidOptions, i)
		}
	}
	if err := validateFloat("temperature", o.Temperature, minimumTemperature, maximumTemperature); err != nil {
		return err
	}
	if err := validatePositive("top_k", o.TopK); err != nil {
		return err
	}
	if err := validateFloat("top_p", o.TopP, minimumTopP, maximumTopP); err != nil {
		return err
	}
	if err := o.Extensions.Validate(); err != nil {
		return fmt.Errorf("%w: extensions: %w", ErrInvalidOptions, err)
	}
	return nil
}

func validatePositive(name string, value *int64) error {
	if value != nil && *value <= 0 {
		return fmt.Errorf("%w: %s must be greater than zero", ErrInvalidOptions, name)
	}
	return nil
}

func validateFloat(name string, value *float64, minValue, maxValue float64) error {
	if value == nil {
		return nil
	}
	if math.IsNaN(*value) || math.IsInf(*value, 0) || *value < minValue || *value > maxValue {
		return fmt.Errorf("%w: %s must be between %g and %g", ErrInvalidOptions, name, minValue, maxValue)
	}
	return nil
}

func (o Options) MarshalJSON() ([]byte, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	type wireOptions Options
	return jsonv2.Marshal(wireOptions(o), jsonv2.Deterministic(true))
}

func (o *Options) UnmarshalJSON(data []byte) error {
	if o == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidOptions)
	}
	type wireOptions Options
	var decoded wireOptions
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidOptions, err)
	}
	candidate := Options(decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*o = candidate
	return nil
}
