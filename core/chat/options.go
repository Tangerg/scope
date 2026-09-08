package chat

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/Tangerg/scope/core/internal/ptr"
	"github.com/Tangerg/scope/core/metadata"
)

// ErrInvalidOptions identifies generation overrides that cannot be mapped to
// the portable request contract.
var ErrInvalidOptions = errors.New("chat: invalid options")

const (
	minimumPenalty     = -2.0
	maximumPenalty     = 2.0
	minimumTemperature = 0.0
	maximumTemperature = 2.0
	minimumTopP        = 0.0
	maximumTopP        = 1.0
)

// Options contains provider-neutral per-request generation overrides. Its zero
// value means provider defaults. Resolve overlays only explicitly populated
// fields, merges namespaced extensions, snapshots mutable values, and leaves
// both source values unchanged.
//
// A populated field an adapter cannot express must be reported, never dropped.
// A caller has no other way to learn the difference: a request that silently
// loses its temperature or its reasoning effort still returns a plausible
// answer, generated under settings nobody asked for. Refusing costs the caller
// one error at the boundary and tells them exactly which of their intentions
// the provider cannot carry, which is why an unsupported option is a provider
// capability gap rather than a value to approximate.
type Options struct {
	Model            string              `json:"model,omitempty"`
	OutputFormat     *OutputFormat       `json:"output_format,omitempty"`
	FrequencyPenalty *float64            `json:"frequency_penalty,omitempty"`
	MaxOutputTokens  *int64              `json:"max_output_tokens,omitempty"`
	PresencePenalty  *float64            `json:"presence_penalty,omitempty"`
	ReasoningEffort  ReasoningEffort     `json:"reasoning_effort,omitempty"`
	Stop             []string            `json:"stop,omitzero"`
	Temperature      *float64            `json:"temperature,omitempty"`
	TopK             *int64              `json:"top_k,omitempty"`
	TopP             *float64            `json:"top_p,omitempty"`
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
	if o.Model != "" && strings.TrimSpace(o.Model) != o.Model {
		return fmt.Errorf("%w: model must not have surrounding whitespace", ErrInvalidOptions)
	}
	if o.OutputFormat != nil {
		if err := o.OutputFormat.Validate(); err != nil {
			return fmt.Errorf("%w: output_format: %w", ErrInvalidOptions, err)
		}
	}
	if err := validateFloat("frequency_penalty", o.FrequencyPenalty, minimumPenalty, maximumPenalty); err != nil {
		return err
	}
	if o.MaxOutputTokens != nil && *o.MaxOutputTokens <= 0 {
		return fmt.Errorf("%w: max_output_tokens must be greater than zero", ErrInvalidOptions)
	}
	if err := validateFloat("presence_penalty", o.PresencePenalty, minimumPenalty, maximumPenalty); err != nil {
		return err
	}
	if err := o.ReasoningEffort.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidOptions, err)
	}
	for i, stop := range o.Stop {
		if stop == "" {
			return fmt.Errorf("%w: stop[%d] must not be empty", ErrInvalidOptions, i)
		}
	}
	if err := validateFloat("temperature", o.Temperature, minimumTemperature, maximumTemperature); err != nil {
		return err
	}
	if o.TopK != nil && *o.TopK <= 0 {
		return fmt.Errorf("%w: top_k must be greater than zero", ErrInvalidOptions)
	}
	if err := validateFloat("top_p", o.TopP, minimumTopP, maximumTopP); err != nil {
		return err
	}
	if err := o.Extensions.Validate(); err != nil {
		return fmt.Errorf("%w: extensions: %w", ErrInvalidOptions, err)
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
	return json.Marshal(wireOptions(o))
}

func (o *Options) UnmarshalJSON(data []byte) error {
	if o == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidOptions)
	}
	type wireOptions Options
	var decoded wireOptions
	if err := json.Unmarshal(data, &decoded); err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidOptions, err)
	}
	candidate := Options(decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*o = candidate
	return nil
}
