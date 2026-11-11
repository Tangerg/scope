package deepseek

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"regexp"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/metadata"
	toolcontract "github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/models/protocol/openai"
)

const (
	thinkingField          = "thinking"
	reasoningEffortField   = "reasoning_effort"
	responseFormatField    = "response_format"
	toolChoiceField        = "tool_choice"
	parallelToolCallsField = "parallel_tool_calls"
	logProbsField          = "logprobs"
	topLogProbsField       = "top_logprobs"
	streamOptionsField     = "stream_options"
	includeUsageField      = "include_usage"
	userIDField            = "user_id"
)

const (
	maximumStopSequences     = 16
	maximumTools             = 128
	maximumTopLogProbs       = 20
	maximumUserIDLength      = 512
	minimumFlashThinkingTopP = 0.95
	reasoningEffortNone      = corechat.ReasoningEffort("none")
	reasoningEffortMinimal   = corechat.ReasoningEffort("minimal")
	reasoningEffortMedium    = corechat.ReasoningEffort("medium")
	reasoningEffortXHigh     = corechat.ReasoningEffort("xhigh")
	reasoningEffortLow       = corechat.ReasoningEffort("low")
	reasoningEffortHigh      = corechat.ReasoningEffort("high")
	reasoningEffortMax       = corechat.ReasoningEffort("max")
)

// Namespacing preserves provider-specific data without promoting it into the
// shared Core protocol or colliding with another provider.
const RequestExtensionKey = Provider + "/request"

var userIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// RequestOptions contains documented DeepSeek Chat Completions fields that
// have no provider-neutral Core equivalent. Store it in
// [chat.Options.Extensions] under [RequestExtensionKey].
type RequestOptions struct {
	LogProbs     *bool  `json:"logprobs,omitzero"`
	TopLogProbs  *int64 `json:"top_logprobs,omitzero"`
	IncludeUsage *bool  `json:"include_usage,omitzero"`
	UserID       string `json:"user_id,omitempty"`
}

func (r *RequestOptions) UnmarshalJSON(data []byte) error {
	if r == nil {
		return errors.New("deepseek: nil RequestOptions")
	}
	var fields map[string]json.RawMessage
	if err := jsonv2.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, field := range []string{thinkingField, reasoningEffortField} {
		if _, exists := fields[field]; exists {
			return fmt.Errorf("field %q belongs to options.reasoning_effort: %w", field, metadata.ErrCoreOwnedField)
		}
	}
	if _, exists := fields[responseFormatField]; exists {
		return fmt.Errorf("field \"response_format\" belongs to options.output_format: %w", metadata.ErrCoreOwnedField)
	}
	for _, field := range []string{toolChoiceField, parallelToolCallsField} {
		if _, exists := fields[field]; exists {
			return fmt.Errorf("field %q belongs to options.tool_choice: %w", field, metadata.ErrCoreOwnedField)
		}
	}
	type wireOptions RequestOptions
	var decoded wireOptions
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return err
	}
	*r = RequestOptions(decoded)
	return nil
}

func prepareRequest(request *corechat.Request, target *openai.CompatibleRequest) error {
	options, _, err := request.Options.Extensions.Decode[RequestOptions](RequestExtensionKey)
	if err != nil {
		return fmt.Errorf("extension %q: %w", RequestExtensionKey, err)
	}
	if err := options.ValidateFor(request.Options, request.Tools, target.Stream()); err != nil {
		return err
	}
	if choice := request.ToolChoice; request.Options.ReasoningEffort != reasoningEffortNone && choice != nil && (choice.Mode == corechat.ToolChoiceRequired || choice.Mode == corechat.ToolChoiceNamed) {
		return errors.New("options.tool_choice required and named modes are not supported while DeepSeek thinking is enabled")
	}
	if options.LogProbs != nil {
		if err := target.SetExtraField(logProbsField, *options.LogProbs); err != nil {
			return err
		}
	}
	if options.TopLogProbs != nil {
		if err := target.SetExtraField(topLogProbsField, *options.TopLogProbs); err != nil {
			return err
		}
	}
	if options.IncludeUsage != nil {
		if err := target.SetExtraField(streamOptionsField, map[string]bool{
			includeUsageField: *options.IncludeUsage,
		}); err != nil {
			return err
		}
	}
	if options.UserID != "" {
		if err := target.SetExtraField(userIDField, options.UserID); err != nil {
			return err
		}
	}
	return nil
}

func (r RequestOptions) ValidateFor(generation corechat.Options, tools []toolcontract.Definition, stream bool) error {
	thinkingEnabled := generation.ReasoningEffort != reasoningEffortNone
	switch generation.ReasoningEffort {
	case "", reasoningEffortNone, reasoningEffortMinimal, reasoningEffortLow, reasoningEffortMedium, reasoningEffortHigh, reasoningEffortXHigh, reasoningEffortMax:
	default:
		return fmt.Errorf("options.reasoning_effort has unsupported value %q", generation.ReasoningEffort)
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
	if generation.TopP != nil {
		if err := validateTopP(generation.Model, thinkingEnabled, *generation.TopP); err != nil {
			return err
		}
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

func validateTopP(model string, thinkingEnabled bool, value float64) error {
	switch model {
	case "":
		// Defaults may leave the model to the request. Admission checks the
		// resolved model before dispatch instead of guessing its sampling policy.
		return nil
	case ModelFlash:
		if !thinkingEnabled {
			return errors.New("options.top_p has no effect while DeepSeek Flash thinking is disabled")
		}
		if value < minimumFlashThinkingTopP {
			return fmt.Errorf("options.top_p must be at least %g while DeepSeek Flash thinking is enabled", minimumFlashThinkingTopP)
		}
	case ModelV4Pro:
		if thinkingEnabled {
			return errors.New("options.top_p has no effect while DeepSeek V4 Pro thinking is enabled")
		}
	default:
		return fmt.Errorf("options.top_p is unsupported for model %q: its sampling policy is unknown", model)
	}
	return nil
}
