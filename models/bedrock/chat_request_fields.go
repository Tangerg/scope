package bedrock

import (
	"fmt"

	"github.com/Tangerg/scope/core/metadata"
)

const (
	nativeFieldModel                = "model"
	nativeFieldModelIDSnake         = "model_id"
	nativeFieldModelIDCamel         = "modelId"
	nativeFieldMessages             = "messages"
	nativeFieldMessage              = "message"
	nativeFieldChatHistory          = "chat_history"
	nativeFieldPrompt               = "prompt"
	nativeFieldInputText            = "inputText"
	nativeFieldSystem               = "system"
	nativeFieldPreamble             = "preamble"
	nativeFieldTools                = "tools"
	nativeFieldToolResults          = "tool_results"
	nativeFieldToolChoiceSnake      = "tool_choice"
	nativeFieldToolChoiceCamel      = "toolChoice"
	nativeFieldToolConfigSnake      = "tool_config"
	nativeFieldToolConfigCamel      = "toolConfig"
	nativeFieldParallelToolCalls    = "parallel_tool_calls"
	nativeFieldTemperature          = "temperature"
	nativeFieldTopPSnake            = "top_p"
	nativeFieldTopPCamel            = "topP"
	nativeFieldTopPShort            = "p"
	nativeFieldTopKSnake            = "top_k"
	nativeFieldTopKCamel            = "topK"
	nativeFieldTopKShort            = "k"
	nativeFieldMaxTokensSnake       = "max_tokens"
	nativeFieldMaxTokensCamel       = "maxTokens"
	nativeFieldMaxCompletionTokens  = "max_completion_tokens"
	nativeFieldMaxOutputTokens      = "max_output_tokens"
	nativeFieldMaxTokenCount        = "maxTokenCount"
	nativeFieldMaxGenerationLength  = "max_gen_len"
	nativeFieldMaxTokensToSample    = "max_tokens_to_sample"
	nativeFieldStop                 = "stop"
	nativeFieldStopSequencesSnake   = "stop_sequences"
	nativeFieldStopSequencesCamel   = "stopSequences"
	nativeFieldFrequencyPenalty     = "frequency_penalty"
	nativeFieldPresencePenalty      = "presence_penalty"
	nativeFieldReasoningEffortSnake = "reasoning_effort"
	nativeFieldReasoningEffortCamel = "reasoningEffort"
	nativeFieldResponseFormatSnake  = "response_format"
	nativeFieldResponseFormatCamel  = "responseFormat"
	nativeFieldOutputConfigCamel    = "outputConfig"
	nativeFieldInferenceConfig      = "inferenceConfig"
	nativeFieldTextGenerationConfig = "textGenerationConfig"
	nativeFieldOutputConfigSnake    = "output_config"
	nativeFieldEffort               = "effort"
	nativeFieldFormat               = "format"
	nativeFieldReasoningConfig      = "reasoningConfig"
	nativeFieldMaxReasoningEffort   = "maxReasoningEffort"
)

// Native model fields must not create a second input for a Core fact, even
// when Converse cannot currently project that option for the selected model.
func validateAdditionalModelRequestFields(fields metadata.Map) error {
	for name := range fields {
		var coreMembers []string
		switch name {
		case nativeFieldModel, nativeFieldModelIDSnake, nativeFieldModelIDCamel,
			nativeFieldMessages, nativeFieldMessage, nativeFieldChatHistory, nativeFieldPrompt, nativeFieldInputText, nativeFieldSystem, nativeFieldPreamble,
			nativeFieldTools, nativeFieldToolResults, nativeFieldToolChoiceSnake, nativeFieldToolChoiceCamel, nativeFieldToolConfigSnake, nativeFieldToolConfigCamel, nativeFieldParallelToolCalls,
			nativeFieldTemperature, nativeFieldTopPSnake, nativeFieldTopPCamel, nativeFieldTopPShort, nativeFieldTopKSnake, nativeFieldTopKCamel, nativeFieldTopKShort,
			nativeFieldMaxTokensSnake, nativeFieldMaxTokensCamel, nativeFieldMaxCompletionTokens, nativeFieldMaxOutputTokens, nativeFieldMaxTokenCount, nativeFieldMaxGenerationLength, nativeFieldMaxTokensToSample,
			nativeFieldStop, nativeFieldStopSequencesSnake, nativeFieldStopSequencesCamel,
			nativeFieldFrequencyPenalty, nativeFieldPresencePenalty, nativeFieldReasoningEffortSnake, nativeFieldReasoningEffortCamel,
			nativeFieldResponseFormatSnake, nativeFieldResponseFormatCamel, nativeFieldOutputConfigCamel, nativeFieldInferenceConfig, nativeFieldTextGenerationConfig:
			return fmt.Errorf("bedrock: additional_model_request_fields.%s is owned by Core", name)
		case nativeFieldOutputConfigSnake:
			coreMembers = []string{nativeFieldEffort, nativeFieldFormat}
		case nativeFieldReasoningConfig:
			coreMembers = []string{nativeFieldMaxReasoningEffort}
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
