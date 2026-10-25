package openai

type responsesRequestField string

const (
	responsesFieldModel           responsesRequestField = "model"
	responsesFieldInput           responsesRequestField = "input"
	responsesFieldInstructions    responsesRequestField = "instructions"
	responsesFieldTools           responsesRequestField = "tools"
	responsesFieldMaxTokens       responsesRequestField = "max_output_tokens"
	responsesFieldTemperature     responsesRequestField = "temperature"
	responsesFieldTopP            responsesRequestField = "top_p"
	responsesFieldStream          responsesRequestField = "stream"
	responsesFieldParallelTools   responsesRequestField = "parallel_tool_calls"
	responsesFieldToolChoice      responsesRequestField = "tool_choice"
	responsesFieldReasoning       responsesRequestField = "reasoning"
	responsesFieldText            responsesRequestField = "text"
	responsesFieldInclude         responsesRequestField = "include"
	responsesFieldStore           responsesRequestField = "store"
	responsesFieldReasoningEffort responsesRequestField = "effort"
	responsesFieldTextFormat      responsesRequestField = "format"
)

func (r responsesRequestField) coreOwned() bool {
	switch r {
	case responsesFieldModel, responsesFieldInput, responsesFieldInstructions, responsesFieldTools,
		responsesFieldMaxTokens, responsesFieldTemperature, responsesFieldTopP, responsesFieldStream,
		responsesFieldParallelTools, responsesFieldToolChoice:
		return true
	default:
		return false
	}
}

func (r responsesRequestField) typedOwner() bool {
	if r.coreOwned() {
		return true
	}
	switch r {
	case responsesFieldReasoning, responsesFieldText, responsesFieldInclude, responsesFieldStore:
		return true
	default:
		return false
	}
}
