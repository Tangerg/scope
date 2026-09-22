package deepseek

import (
	"errors"
	"fmt"
	"regexp"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/protocol/openai"
)

const (
	maximumStopSequences = 16
	maximumTools         = 128
	maximumTopLogProbs   = 20
	maximumUserIDLength  = 512
	reasoningEffortLow   = corechat.ReasoningEffort("low")
	reasoningEffortHigh  = corechat.ReasoningEffort("high")
	reasoningEffortMax   = corechat.ReasoningEffort("max")
)

// Namespacing preserves provider-specific data without promoting it into the
// shared Core protocol or colliding with another provider.
const RequestExtensionKey = "deepseek/request"

var userIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ThinkingMode selects whether DeepSeek emits reasoning_content before its
// final answer. The API defaults to enabled when the field is omitted.
type ThinkingMode string

// These are the provider values this adapter recognizes.
const (
	ThinkingEnabled  ThinkingMode = "enabled"
	ThinkingDisabled ThinkingMode = "disabled"
)

// ThinkingConfig controls DeepSeek's hybrid thinking mode.
type ThinkingConfig struct {
	Type ThinkingMode `json:"type"`
}

func (t ThinkingConfig) Validate() error {
	switch t.Type {
	case ThinkingEnabled, ThinkingDisabled:
		return nil
	default:
		return fmt.Errorf("thinking.type has unsupported value %q", t.Type)
	}
}

// RequestOptions contains documented DeepSeek Chat Completions fields that
// have no provider-neutral Core equivalent. Store it in
// [chat.Options.Extensions] under [RequestExtensionKey].
type RequestOptions struct {
	Thinking     *ThinkingConfig `json:"thinking,omitzero"`
	LogProbs     *bool           `json:"logprobs,omitzero"`
	TopLogProbs  *int64          `json:"top_logprobs,omitzero"`
	IncludeUsage *bool           `json:"include_usage,omitzero"`
	UserID       string          `json:"user_id,omitempty"`
}

type requestDialect struct {
	defaults corechat.Options
}

func (r requestDialect) prepareRequest(request *corechat.Request, target *openai.CompatibleRequest) error {
	fields, _, err := request.Options.Extensions.Decode[map[string]any](RequestExtensionKey)
	if err != nil {
		return fmt.Errorf("extension %q: %w", RequestExtensionKey, err)
	}
	if _, exists := fields["response_format"]; exists {
		return fmt.Errorf("extension %q field %q is owned by options.output_format", RequestExtensionKey, "response_format")
	}
	if _, exists := fields["reasoning_effort"]; exists {
		return fmt.Errorf("extension %q field %q is owned by options.reasoning_effort", RequestExtensionKey, "reasoning_effort")
	}
	for _, field := range []string{"tool_choice", "parallel_tool_calls"} {
		if _, exists := fields[field]; exists {
			return fmt.Errorf("extension %q field %q is owned by options.tool_choice", RequestExtensionKey, field)
		}
	}
	options, _, err := request.Options.Extensions.Decode[RequestOptions](RequestExtensionKey)
	if err != nil {
		return fmt.Errorf("extension %q: %w", RequestExtensionKey, err)
	}
	effective, err := r.defaults.Resolve(request.Options)
	if err != nil {
		return fmt.Errorf("options: %w", err)
	}
	if err := options.ValidateFor(effective, request.Tools, target.Stream()); err != nil {
		return err
	}

	if options.Thinking != nil {
		if err := target.SetExtraField("thinking", options.Thinking); err != nil {
			return err
		}
	}
	if options.LogProbs != nil {
		if err := target.SetExtraField("logprobs", *options.LogProbs); err != nil {
			return err
		}
	}
	if options.TopLogProbs != nil {
		if err := target.SetExtraField("top_logprobs", *options.TopLogProbs); err != nil {
			return err
		}
	}
	if options.IncludeUsage != nil {
		if err := target.SetExtraField("stream_options", map[string]bool{
			"include_usage": *options.IncludeUsage,
		}); err != nil {
			return err
		}
	}
	if options.UserID != "" {
		if err := target.SetExtraField("user_id", options.UserID); err != nil {
			return err
		}
	}
	return nil
}

func (r RequestOptions) ValidateFor(generation corechat.Options, tools []corechat.ToolDefinition, stream bool) error {
	thinkingEnabled := r.Thinking == nil || r.Thinking.Type != ThinkingDisabled
	if r.Thinking != nil {
		if err := r.Thinking.Validate(); err != nil {
			return err
		}
	}
	switch generation.ReasoningEffort {
	case "", reasoningEffortLow, reasoningEffortHigh, reasoningEffortMax:
	default:
		return fmt.Errorf("options.reasoning_effort has unsupported value %q", generation.ReasoningEffort)
	}
	if !thinkingEnabled && generation.ReasoningEffort != "" {
		return errors.New("options.reasoning_effort requires thinking.type=enabled")
	}
	if generation.FrequencyPenalty != nil {
		return errors.New("options.frequency_penalty is deprecated and unsupported by DeepSeek")
	}
	if generation.PresencePenalty != nil {
		return errors.New("options.presence_penalty is deprecated and unsupported by DeepSeek")
	}
	if thinkingEnabled && generation.Temperature != nil {
		return errors.New("options.temperature has no effect while DeepSeek thinking is enabled")
	}
	if thinkingEnabled && generation.TopP != nil {
		return errors.New("options.top_p has no effect while DeepSeek thinking is enabled")
	}
	if len(generation.Stop) > maximumStopSequences {
		return fmt.Errorf("options.stop must contain at most %d sequences for DeepSeek", maximumStopSequences)
	}
	if len(tools) > maximumTools {
		return fmt.Errorf("tools must contain at most %d functions for DeepSeek", maximumTools)
	}

	if r.TopLogProbs != nil {
		if *r.TopLogProbs < 0 || *r.TopLogProbs > maximumTopLogProbs {
			return fmt.Errorf("top_logprobs must be between 0 and %d", maximumTopLogProbs)
		}
		if r.LogProbs == nil || !*r.LogProbs {
			return errors.New("top_logprobs requires logprobs=true")
		}
	}
	if r.IncludeUsage != nil && !stream {
		return errors.New("include_usage is valid only for streaming requests")
	}
	if r.UserID != "" {
		if len(r.UserID) > maximumUserIDLength {
			return fmt.Errorf("user_id must contain at most %d characters", maximumUserIDLength)
		}
		if !userIDPattern.MatchString(r.UserID) {
			return errors.New("user_id may contain only ASCII letters, digits, hyphens, and underscores")
		}
	}
	return nil
}
