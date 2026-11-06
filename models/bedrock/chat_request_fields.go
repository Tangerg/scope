package bedrock

import (
	"fmt"

	"github.com/Tangerg/scope/core/metadata"
)

// Native model fields must not create a second input for a Core fact, even
// when Converse cannot currently project that option for the selected model.
func validateAdditionalModelRequestFields(fields metadata.Map) error {
	for name := range fields {
		var coreMembers []string
		switch name {
		case "model", "model_id", "modelId",
			"messages", "message", "chat_history", "prompt", "inputText", "system", "preamble",
			"tools", "tool_results", "tool_choice", "toolChoice", "tool_config", "toolConfig", "parallel_tool_calls",
			"temperature", "top_p", "topP", "p", "top_k", "topK", "k",
			"max_tokens", "maxTokens", "max_completion_tokens", "max_output_tokens", "maxTokenCount", "max_gen_len", "max_tokens_to_sample",
			"stop", "stop_sequences", "stopSequences",
			"frequency_penalty", "presence_penalty", "reasoning_effort", "reasoningEffort",
			"response_format", "responseFormat", "outputConfig", "inferenceConfig", "textGenerationConfig":
			return fmt.Errorf("bedrock: additional_model_request_fields.%s is owned by Core", name)
		case "output_config":
			coreMembers = []string{"effort", "format"}
		case "reasoningConfig":
			coreMembers = []string{"maxReasoningEffort"}
		default:
			continue
		}
		members, _, err := fields.Decode[metadata.Map](name)
		if err != nil {
			return fmt.Errorf("bedrock: additional_model_request_fields.%s: %w", name, err)
		}
		for _, member := range coreMembers {
			if _, exists := members[member]; exists {
				return fmt.Errorf("bedrock: additional_model_request_fields.%s.%s is owned by Core", name, member)
			}
		}
	}
	return nil
}
