package minimax

import (
	"bytes"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/protocol/openai"
)

const (
	RequestExtensionKey = Provider + "/request"
	reasoningSplitField = "reasoning_split"
	thinkingField       = "thinking"
	serviceTierField    = "service_tier"
)

const (
	reasoningEffortLow    corechat.ReasoningEffort = "low"
	reasoningEffortMedium corechat.ReasoningEffort = "medium"
	reasoningEffortHigh   corechat.ReasoningEffort = "high"
	reasoningEffortXHigh  corechat.ReasoningEffort = "xhigh"
	reasoningEffortMax    corechat.ReasoningEffort = "max"
)

// ThinkingType controls MiniMax-M3 thinking. M2.x models ignore disabled and
// always think according to the official protocol.
type ThinkingType string

const (
	ThinkingAdaptive ThinkingType = "adaptive"
	ThinkingDisabled ThinkingType = "disabled"
)

type thinkingConfig struct {
	Type ThinkingType `json:"type"`
}

type ServiceTier string

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

// MiniMax otherwise embeds thinking in answer content inside <think> tags.
func prepareOpenAIRequest(source *corechat.Request, target *openai.CompatibleRequest) error {
	if err := openai.RequestStreamUsage(source, target); err != nil {
		return err
	}
	if err := validateReasoningEffort(target.Model(), source.Options.ReasoningEffort); err != nil {
		return err
	}
	extension, found, err := source.Options.Extensions.Decode[ChatRequestOptions](RequestExtensionKey)
	if err != nil {
		return fmt.Errorf("minimax: extension %q: %w", RequestExtensionKey, err)
	}
	if found {
		if err := extension.Validate(); err != nil {
			return fmt.Errorf("minimax: extension %q: %w", RequestExtensionKey, err)
		}
		if err := extension.validateFor(target.Model()); err != nil {
			return err
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
		if err := target.SetExtraField(thinkingField, thinkingConfig{Type: extension.Thinking}); err != nil {
			return err
		}
	}
	if found && extension.ServiceTier != "" {
		if err := target.SetExtraField(serviceTierField, extension.ServiceTier); err != nil {
			return err
		}
	}
	return nil
}

func prepareAnthropicRequest(source *corechat.Request, fields map[string]any) error {
	if choice := source.ToolChoice; choice != nil && choice.Mode != corechat.ToolChoiceAuto {
		return fmt.Errorf("minimax: Messages tool choice %q: %w", choice.Mode, errors.ErrUnsupported)
	}
	if err := validateReasoningEffort(source.Options.Model, source.Options.ReasoningEffort); err != nil {
		return err
	}
	encoded, err := jsonv2.Marshal(fields)
	if err != nil {
		return err
	}
	var native struct {
		Thinking    *thinkingConfig `json:"thinking"`
		ServiceTier ServiceTier     `json:"service_tier"`
	}
	if err := jsonv2.Unmarshal(encoded, &native); err != nil {
		return fmt.Errorf("minimax: model controls: %w", err)
	}
	controls := ChatRequestOptions{ServiceTier: native.ServiceTier}
	if native.Thinking != nil {
		controls.Thinking = native.Thinking.Type
	}
	if err := controls.Validate(); err != nil {
		return err
	}
	if err := controls.validateFor(source.Options.Model); err != nil {
		return err
	}
	switch {
	case source.Options.Temperature != nil:
		return errors.New("minimax: Messages ignores options.temperature")
	case source.Options.TopP != nil:
		return errors.New("minimax: Messages ignores options.top_p")
	case source.Options.TopK != nil:
		return errors.New("minimax: Messages ignores options.top_k")
	case len(source.Options.Stop) != 0:
		return errors.New("minimax: Messages ignores options.stop")
	default:
		return nil
	}
}

func validateReasoningEffort(model string, effort corechat.ReasoningEffort) error {
	if effort == "" {
		return nil
	}
	if model != ModelM31Flash {
		return fmt.Errorf("minimax: model %q does not support options.reasoning_effort", model)
	}
	switch effort {
	case reasoningEffortLow, reasoningEffortMedium, reasoningEffortHigh, reasoningEffortXHigh, reasoningEffortMax:
		return nil
	default:
		return fmt.Errorf("minimax: model %q does not support reasoning effort %q", model, effort)
	}
}

func (c ChatRequestOptions) validateFor(model string) error {
	if model != ModelM31Flash {
		return nil
	}
	switch {
	case c.Thinking == ThinkingDisabled:
		return fmt.Errorf("minimax: model %q requires adaptive thinking", model)
	case c.ReasoningSplit != nil && !*c.ReasoningSplit:
		return fmt.Errorf("minimax: model %q requires reasoning_split", model)
	case c.ServiceTier != "":
		return fmt.Errorf("minimax: model %q does not support service_tier", model)
	default:
		return nil
	}
}

func (c *ChatRequestOptions) UnmarshalJSON(data []byte) error {
	if c == nil {
		return errors.New("minimax: nil ChatRequestOptions")
	}
	raw := bytes.TrimSpace(data)
	if len(raw) == 0 || raw[0] != '{' {
		return errors.New("minimax: request options must be an object")
	}
	type wireOptions ChatRequestOptions
	var decoded wireOptions
	if err := jsonv2.Unmarshal(raw, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return err
	}
	candidate := ChatRequestOptions(decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*c = candidate
	return nil
}
