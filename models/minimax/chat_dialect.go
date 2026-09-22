package minimax

import (
	"fmt"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/protocol/openai"
)

const (
	// RequestExtensionKey stores MiniMax-specific Chat Completions options in a
	// Core request.
	RequestExtensionKey = "minimax/request"
	reasoningSplitField = "reasoning_split"
)

// ThinkingType controls MiniMax-M3 thinking. M2.x models ignore disabled and
// always think according to the official protocol.
type ThinkingType string

// These are the provider values this adapter recognizes.
const (
	ThinkingAdaptive ThinkingType = "adaptive"
	ThinkingDisabled ThinkingType = "disabled"
)

// ServiceTier controls MiniMax request admission priority.
type ServiceTier string

// These are the provider values this adapter recognizes.
const (
	ServiceTierStandard ServiceTier = "standard"
	ServiceTierPriority ServiceTier = "priority"
)

// ChatRequestOptions contains MiniMax request fields without a provider-neutral
// Core equivalent.
type ChatRequestOptions struct {
	Thinking       ThinkingType `json:"thinking,omitempty"`
	ReasoningSplit *bool        `json:"reasoning_split,omitzero"`
	ServiceTier    ServiceTier  `json:"service_tier,omitempty"`
}

func (c ChatRequestOptions) Validate() error {
	switch c.Thinking {
	case "", ThinkingAdaptive, ThinkingDisabled:
	default:
		return fmt.Errorf("thinking must be %q or %q", ThinkingAdaptive, ThinkingDisabled)
	}
	switch c.ServiceTier {
	case "", ServiceTierStandard, ServiceTierPriority:
	default:
		return fmt.Errorf("service_tier must be %q or %q", ServiceTierStandard, ServiceTierPriority)
	}
	return nil
}

// reasoningSplitDialect keeps MiniMax thinking separate from answer content.
// The official API otherwise embeds thinking in content inside <think> tags.
func prepareOpenAIRequest(source *corechat.Request, target *openai.CompatibleRequest) error {
	extension, found, err := source.Options.Extensions.Decode[ChatRequestOptions](RequestExtensionKey)
	if err != nil {
		return fmt.Errorf("minimax: extension %q: %w", RequestExtensionKey, err)
	}
	if found {
		if err := extension.Validate(); err != nil {
			return fmt.Errorf("minimax: extension %q: %w", RequestExtensionKey, err)
		}
	}
	reasoningSplit := true
	if found && extension.ReasoningSplit != nil {
		reasoningSplit = *extension.ReasoningSplit
	}
	if err := target.SetExtraField(reasoningSplitField, reasoningSplit); err != nil {
		return err
	}
	if found && extension.Thinking != "" {
		if err := target.SetExtraField("thinking", map[string]any{"type": extension.Thinking}); err != nil {
			return err
		}
	}
	if found && extension.ServiceTier != "" {
		if err := target.SetExtraField("service_tier", extension.ServiceTier); err != nil {
			return err
		}
	}
	return nil
}
