package moonshot

import (
	"fmt"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/protocol/openai"
)

// Namespacing preserves provider-specific data without promoting it into the
// shared Core protocol or colliding with another provider.
const RequestExtensionKey = "moonshot/request"

// ThinkingType controls thinking for Kimi K2.x models.
type ThinkingType string

// These are the provider values this adapter recognizes.
const (
	ThinkingEnabled  ThinkingType = "enabled"
	ThinkingDisabled ThinkingType = "disabled"
)

// ThinkingKeep controls preserved thinking for Kimi K2.x models.
type ThinkingKeep string

// ThinkingKeepAll is the only preserved-thinking mode Kimi K2.x accepts. It is
// a named constant so a caller states the intent rather than the vendor's
// spelling.
const ThinkingKeepAll ThinkingKeep = "all"

// ReasoningEffort controls Kimi K3 reasoning intensity.
type ReasoningEffort string

// These are the provider values this adapter recognizes.
const (
	ReasoningEffortLow  ReasoningEffort = "low"
	ReasoningEffortHigh ReasoningEffort = "high"
	ReasoningEffortMax  ReasoningEffort = "max"
)

// Thinking configures Kimi K2.x reasoning.
type Thinking struct {
	Type ThinkingType `json:"type"`
	Keep ThinkingKeep `json:"keep,omitempty"`
}

// ChatRequestOptions contains Kimi Chat Completions fields without a
// provider-neutral Core equivalent.
type ChatRequestOptions struct {
	Thinking         *Thinking       `json:"thinking,omitzero"`
	ReasoningEffort  ReasoningEffort `json:"reasoning_effort,omitempty"`
	PromptCacheKey   string          `json:"prompt_cache_key,omitempty"`
	SafetyIdentifier string          `json:"safety_identifier,omitempty"`
	Partial          *bool           `json:"partial,omitzero"`
}

func (c ChatRequestOptions) ValidateFor(model string) error {
	if c.Thinking != nil {
		switch c.Thinking.Type {
		case ThinkingEnabled, ThinkingDisabled:
		default:
			return fmt.Errorf("thinking.type must be %q or %q", ThinkingEnabled, ThinkingDisabled)
		}
		switch c.Thinking.Keep {
		case "", ThinkingKeepAll:
		default:
			return fmt.Errorf("thinking.keep must be %q when set", ThinkingKeepAll)
		}
		if c.Thinking.Type == ThinkingDisabled && c.Thinking.Keep != "" {
			return fmt.Errorf("thinking.keep requires thinking.type %q", ThinkingEnabled)
		}
	}
	switch c.ReasoningEffort {
	case "", ReasoningEffortLow, ReasoningEffortHigh, ReasoningEffortMax:
	default:
		return fmt.Errorf("reasoning_effort must be %q, %q, or %q", ReasoningEffortLow, ReasoningEffortHigh, ReasoningEffortMax)
	}

	switch model {
	case ModelK3:
		if c.Thinking != nil {
			return fmt.Errorf("model %q does not accept thinking; use reasoning_effort", model)
		}
	case ModelK27Code, ModelK27CodeHighSpeed:
		if c.ReasoningEffort != "" {
			return fmt.Errorf("model %q does not accept reasoning_effort", model)
		}
		if c.Thinking != nil && (c.Thinking.Type != ThinkingEnabled || c.Thinking.Keep != ThinkingKeepAll) {
			return fmt.Errorf("model %q only accepts thinking {type:%q, keep:%q}", model, ThinkingEnabled, ThinkingKeepAll)
		}
	case ModelK26:
		if c.ReasoningEffort != "" {
			return fmt.Errorf("model %q does not accept reasoning_effort", model)
		}
	}
	return nil
}

func prepareOpenAIRequest(source *corechat.Request, target *openai.CompatibleRequest) error {
	options, found, err := source.Options.Extensions.Decode[ChatRequestOptions](RequestExtensionKey)
	if err != nil {
		return fmt.Errorf("moonshot: extension %q: %w", RequestExtensionKey, err)
	}
	if !found {
		return nil
	}
	if err := options.ValidateFor(target.Model()); err != nil {
		return fmt.Errorf("moonshot: extension %q: %w", RequestExtensionKey, err)
	}
	if options.Thinking != nil {
		if err := target.SetExtraField("thinking", options.Thinking); err != nil {
			return err
		}
	}
	if options.ReasoningEffort != "" {
		if err := target.SetExtraField("reasoning_effort", options.ReasoningEffort); err != nil {
			return err
		}
	}
	if options.PromptCacheKey != "" {
		if err := target.SetExtraField("prompt_cache_key", options.PromptCacheKey); err != nil {
			return err
		}
	}
	if options.SafetyIdentifier != "" {
		if err := target.SetExtraField("safety_identifier", options.SafetyIdentifier); err != nil {
			return err
		}
	}
	if options.Partial != nil {
		if err := target.SetExtraField("partial", *options.Partial); err != nil {
			return err
		}
	}
	return nil
}
