package moonshot

import (
	jsonv2 "encoding/json/v2"
	"errors"
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

// Thinking configures Kimi K2.x reasoning.
type Thinking struct {
	Type ThinkingType `json:"type"`
	Keep ThinkingKeep `json:"keep,omitempty"`
}

// ChatRequestOptions contains Kimi Chat Completions fields without a
// provider-neutral Core equivalent.
type ChatRequestOptions struct {
	Thinking         *Thinking `json:"thinking,omitzero"`
	PromptCacheKey   string    `json:"prompt_cache_key,omitempty"`
	SafetyIdentifier string    `json:"safety_identifier,omitempty"`
	Partial          *bool     `json:"partial,omitzero"`
}

func (c ChatRequestOptions) ValidateFor(options corechat.Options) error {
	model := options.Model
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
	switch options.ReasoningEffort {
	case "", "low", "high", "max":
	default:
		return fmt.Errorf("options.reasoning_effort has unsupported value %q", options.ReasoningEffort)
	}

	switch model {
	case ModelK3:
		if c.Thinking != nil {
			return fmt.Errorf("model %q does not accept thinking; use reasoning_effort", model)
		}
	case ModelK27Code, ModelK27CodeHighSpeed:
		if options.ReasoningEffort != "" {
			return fmt.Errorf("model %q does not accept reasoning_effort", model)
		}
		if c.Thinking != nil && (c.Thinking.Type != ThinkingEnabled || c.Thinking.Keep != ThinkingKeepAll) {
			return fmt.Errorf("model %q only accepts thinking {type:%q, keep:%q}", model, ThinkingEnabled, ThinkingKeepAll)
		}
	case ModelK26:
		if options.ReasoningEffort != "" {
			return fmt.Errorf("model %q does not accept reasoning_effort", model)
		}
	}
	return nil
}

func prepareOpenAIRequest(source *corechat.Request, target *openai.CompatibleRequest) error {
	options, _, err := source.Options.Extensions.Decode[ChatRequestOptions](RequestExtensionKey)
	if err != nil {
		return fmt.Errorf("moonshot: extension %q: %w", RequestExtensionKey, err)
	}
	if err := options.ValidateFor(source.Options); err != nil {
		return fmt.Errorf("moonshot: extension %q: %w", RequestExtensionKey, err)
	}
	if options.Thinking != nil {
		if err := target.SetExtraField("thinking", options.Thinking); err != nil {
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

func (c *ChatRequestOptions) UnmarshalJSON(data []byte) error {
	if c == nil {
		return errors.New("moonshot: nil ChatRequestOptions")
	}
	type wireOptions ChatRequestOptions
	var decoded wireOptions
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return err
	}
	*c = ChatRequestOptions(decoded)
	return nil
}
