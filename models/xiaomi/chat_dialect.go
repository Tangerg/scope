package xiaomi

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/protocol/openai"
)

// Namespacing preserves provider-specific data without promoting it into the
// shared Core protocol or colliding with another provider.
const (
	RequestExtensionKey = Provider + "/request"
	thinkingField       = "thinking"
	maximumTemperature  = 1.5
	minimumTopP         = 0.01
)

// ThinkingType controls MiMo deep thinking.
type ThinkingType string

// These are the provider values this adapter recognizes.
const (
	ThinkingEnabled  ThinkingType = "enabled"
	ThinkingDisabled ThinkingType = "disabled"
)

type thinkingConfig struct {
	Type ThinkingType `json:"type"`
}

// ChatRequestOptions contains MiMo chat fields shared by its OpenAI and
// Anthropic endpoints. Store it under RequestExtensionKey for either adapter.
type ChatRequestOptions struct {
	Thinking ThinkingType `json:"thinking,omitempty"`
}

func (c ChatRequestOptions) Validate() error {
	switch c.Thinking {
	case "", ThinkingEnabled, ThinkingDisabled:
		return nil
	default:
		return fmt.Errorf("thinking must be %q or %q", ThinkingEnabled, ThinkingDisabled)
	}
}

// validateRequest owns MiMo's mode-dependent rules for both protocol bindings.
func (c ChatRequestOptions) validateRequest(source *corechat.Request) error {
	if choice := source.ToolChoice; choice != nil && choice.Mode != "" && choice.Mode != corechat.ToolChoiceAuto {
		return fmt.Errorf("xiaomi: tool_choice %q is discarded by MiMo, which serves every request as %q", choice.Mode, corechat.ToolChoiceAuto)
	}
	if source.Options.Temperature != nil && *source.Options.Temperature > maximumTemperature {
		return fmt.Errorf("xiaomi: temperature must be between 0 and %g", maximumTemperature)
	}
	if source.Options.TopP != nil && *source.Options.TopP < minimumTopP {
		return fmt.Errorf("xiaomi: top_p must be between %g and 1", minimumTopP)
	}
	if c.Thinking == ThinkingDisabled {
		return nil
	}
	if source.Options.Temperature != nil {
		return errors.New("xiaomi: MiMo forces temperature to 1.0 in thinking mode, so options.temperature would have no effect")
	}
	if source.Options.TopP != nil {
		return errors.New("xiaomi: MiMo forces top_p to 0.95 in thinking mode, so options.top_p would have no effect")
	}
	return nil
}

func (c *ChatRequestOptions) UnmarshalJSON(data []byte) error {
	if c == nil {
		return errors.New("xiaomi: nil ChatRequestOptions")
	}
	type wireOptions ChatRequestOptions
	var decoded wireOptions
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return err
	}
	candidate := ChatRequestOptions(decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*c = candidate
	return nil
}

func decodeRequestOptions(source *corechat.Request, nativeKey string) (ChatRequestOptions, error) {
	fields, _, err := source.Options.Extensions.Decode[map[string]any](nativeKey)
	if err != nil {
		return ChatRequestOptions{}, fmt.Errorf("xiaomi: extension %q: %w", nativeKey, err)
	}
	if _, exists := fields[thinkingField]; exists {
		return ChatRequestOptions{}, fmt.Errorf("xiaomi: extension %q field thinking is owned by %q", nativeKey, RequestExtensionKey)
	}
	options, _, err := source.Options.Extensions.Decode[ChatRequestOptions](RequestExtensionKey)
	if err != nil {
		return ChatRequestOptions{}, fmt.Errorf("xiaomi: extension %q: %w", RequestExtensionKey, err)
	}
	if err := options.validateRequest(source); err != nil {
		return ChatRequestOptions{}, err
	}
	return options, nil
}

func prepareOpenAIRequest(source *corechat.Request, target *openai.CompatibleRequest) error {
	options, err := decodeRequestOptions(source, OpenAIRequestExtensionKey)
	if err != nil {
		return err
	}
	if options.Thinking == "" {
		return nil
	}
	return target.SetExtraField(thinkingField, thinkingConfig{Type: options.Thinking})
}

func prepareAnthropicRequest(source *corechat.Request, fields map[string]any) error {
	options, err := decodeRequestOptions(source, AnthropicRequestExtensionKey)
	if err != nil {
		return err
	}
	if options.Thinking != "" {
		fields[thinkingField] = thinkingConfig{Type: options.Thinking}
	}
	return nil
}
