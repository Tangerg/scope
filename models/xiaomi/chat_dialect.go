package xiaomi

import (
	"errors"
	"fmt"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/protocol/openai"
)

// Namespacing preserves provider-specific data without promoting it into the
// shared Core protocol or colliding with another provider.
const RequestExtensionKey = "xiaomi/request"

// ThinkingType controls MiMo deep thinking.
type ThinkingType string

// These are the provider values this adapter recognizes.
const (
	ThinkingEnabled  ThinkingType = "enabled"
	ThinkingDisabled ThinkingType = "disabled"
)

// ChatRequestOptions contains MiMo Chat Completions fields without a
// provider-neutral Core equivalent.
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

func prepareOpenAIRequest(source *corechat.Request, target *openai.CompatibleRequest) error {
	if temperature, ok := target.Temperature(); ok && temperature > 1.5 {
		return errors.New("xiaomi: temperature must be between 0 and 1.5")
	}
	options, _, err := source.Options.Extensions.Decode[ChatRequestOptions](RequestExtensionKey)
	if err != nil {
		return fmt.Errorf("xiaomi: extension %q: %w", RequestExtensionKey, err)
	}
	if err = options.Validate(); err != nil {
		return fmt.Errorf("xiaomi: extension %q: %w", RequestExtensionKey, err)
	}
	if err = rejectSilentlyDiscardedOptions(source, target, options); err != nil {
		return err
	}
	if options.Thinking == "" {
		return nil
	}
	return target.SetExtraField("thinking", map[string]any{"type": options.Thinking})
}

// rejectSilentlyDiscardedOptions refuses the settings MiMo documents itself as
// throwing away, rather than letting a caller's choice disappear on the way.
//
// Thinking is checked against the documented default rather than the extension
// alone: MiMo's table defaults thinking to enabled, so an absent extension is
// the case where the override happens, not the case to skip.
func rejectSilentlyDiscardedOptions(
	source *corechat.Request,
	target *openai.CompatibleRequest,
	options ChatRequestOptions,
) error {
	// "when tool_choice passes non-auto values, backend defaults to removing
	// the field, model response behavior remains equal to auto mode" -- a
	// caller asking for a named tool would silently get free choice instead.
	if choice := source.ToolChoice; choice != nil && choice.Mode != "" && choice.Mode != corechat.ToolChoiceAuto {
		return fmt.Errorf("xiaomi: tool choice %q is discarded by MiMo, which serves every request as %q",
			choice.Mode, corechat.ToolChoiceAuto)
	}
	if options.Thinking == ThinkingDisabled {
		return nil
	}
	// "in thinking mode, mimo-v2.5-pro, mimo-v2.5 models do not support custom
	// temperature and top_p parameters. Even if passed, actual values forced to
	// defaults 1.0 and 0.95."
	if _, ok := target.Temperature(); ok {
		return errors.New("xiaomi: MiMo forces temperature to 1.0 in thinking mode, so options.temperature would have no effect")
	}
	if _, ok := target.TopP(); ok {
		return errors.New("xiaomi: MiMo forces top_p to 0.95 in thinking mode, so options.top_p would have no effect")
	}
	return nil
}
