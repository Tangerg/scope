package openai

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	openaisdk "github.com/openai/openai-go/v3"

	corechat "github.com/Tangerg/scope/core/chat"
)

func (c *ChatCompletions) applyRequestExtension(req *corechat.Request, params *openaisdk.ChatCompletionNewParams) error {
	if c.dialect.DisableRawRequestExtension {
		return nil
	}

	extensionKey := protocolRequestExtensionKey(c.dialect.Provider)
	fields, err := decodeRequestFields(req.Options.Extensions, extensionKey,
		"model", "messages", "tools", "frequency_penalty", "max_tokens",
		"max_completion_tokens", "parallel_tool_calls", "presence_penalty", "reasoning_effort", "response_format", "stop", "temperature", "tool_choice", "top_p",
	)
	if err != nil {
		return err
	}
	if _, exists := fields["n"]; exists {
		return fmt.Errorf("openai: extension %q field %q is unsupported; Core Chat produces one output", extensionKey, "n")
	}
	params.SetExtraFields(fields)
	return nil
}

func (c *ChatCompletions) applyOptions(options corechat.Options, toolChoice *corechat.ToolChoice, params *openaisdk.ChatCompletionNewParams) error {
	if options.Model == "" {
		return errors.New("openai: model is required in defaults or request options")
	}
	if options.TopK != nil {
		return errors.New("openai: options.top_k is not supported by Chat Completions")
	}
	params.Model = openaisdk.ChatModel(options.Model)
	if options.FrequencyPenalty != nil {
		if c.dialect.ignores(ChatOptionFrequencyPenalty) {
			return newIgnoredOptionError(c.dialect.Provider, ChatOptionFrequencyPenalty)
		}
		params.FrequencyPenalty = openaisdk.Float(*options.FrequencyPenalty)
	}
	if err := c.applyTokenLimit(options.MaxOutputTokens, params); err != nil {
		return err
	}
	if options.PresencePenalty != nil {
		if c.dialect.ignores(ChatOptionPresencePenalty) {
			return newIgnoredOptionError(c.dialect.Provider, ChatOptionPresencePenalty)
		}
		params.PresencePenalty = openaisdk.Float(*options.PresencePenalty)
	}
	if options.ReasoningEffort != "" && c.dialect.ignores(ChatOptionReasoningEffort) {
		return newIgnoredOptionError(c.dialect.Provider, ChatOptionReasoningEffort)
	}
	reasoningEffort, err := mapReasoningEffort(options.ReasoningEffort)
	if err != nil {
		return err
	}
	params.ReasoningEffort = reasoningEffort
	if len(options.Stop) > 0 {
		params.Stop.OfStringArray = slices.Clone(options.Stop)
	}
	if options.Temperature != nil {
		if limit := c.dialect.MaxTemperature; limit != nil && *options.Temperature > *limit {
			return fmt.Errorf("openai: %s documents a temperature range up to %g, so %v is refused rather than sent out of range",
				c.dialect.Provider, *limit, *options.Temperature)
		}
		params.Temperature = openaisdk.Float(*options.Temperature)
	}
	if options.TopP != nil {
		params.TopP = openaisdk.Float(*options.TopP)
	}
	if toolChoice != nil {
		switch toolChoice.Mode {
		case corechat.ToolChoiceAuto, corechat.ToolChoiceNone, corechat.ToolChoiceRequired:
			params.ToolChoice.OfAuto = openaisdk.String(string(toolChoice.Mode))
		case corechat.ToolChoiceNamed:
			params.ToolChoice = openaisdk.ToolChoiceOptionFunctionToolChoice(
				openaisdk.ChatCompletionNamedToolChoiceFunctionParam{Name: toolChoice.Name},
			)
		}
		switch toolChoice.Parallelism {
		case corechat.ToolParallelismAllow:
			params.ParallelToolCalls = openaisdk.Bool(true)
		case corechat.ToolParallelismSingle:
			params.ParallelToolCalls = openaisdk.Bool(false)
		}
	}
	return nil
}

// newIgnoredOptionError reports an option the provider documents as discarded.
// Sending it and letting the provider drop it is indistinguishable, to the
// caller, from this adapter never having mapped it.
func newIgnoredOptionError(provider string, option ChatOption) error {
	return fmt.Errorf("openai: %s ignores %s, so setting it would have no effect", provider, option)
}

func (c *ChatCompletions) applyTokenLimit(limit *int64, params *openaisdk.ChatCompletionNewParams) error {
	if limit == nil {
		return nil
	}
	switch c.dialect.TokenLimitField {
	case TokenLimitMaxTokens:
		params.MaxTokens = openaisdk.Int(*limit)
	case TokenLimitMaxCompletionTokens:
		params.MaxCompletionTokens = openaisdk.Int(*limit)
	default:
		return errors.New("openai: invalid max token field configuration")
	}
	return nil
}

func (c *ChatCompletions) prepareRequest(req *corechat.Request, stream bool, params *openaisdk.ChatCompletionNewParams) error {
	if c.dialect.request != nil {
		if err := c.dialect.request.PrepareRequest(req, params); err != nil {
			return fmt.Errorf("openai: request dialect: %w", err)
		}
	}
	if c.dialect.PrepareRequest == nil {
		return nil
	}

	compatible := &CompatibleRequest{
		model:       string(params.Model),
		stream:      stream,
		extraFields: maps.Clone(params.ExtraFields()),
	}
	if params.Temperature.Valid() {
		compatible.temperature = &params.Temperature.Value
	}
	if params.TopP.Valid() {
		compatible.topP = &params.TopP.Value
	}
	if err := c.dialect.PrepareRequest(req, compatible); err != nil {
		return fmt.Errorf("openai: compatible request dialect: %w", err)
	}
	params.SetExtraFields(compatible.extraFields)
	return nil
}
