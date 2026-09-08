package bedrock

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	corechat "github.com/Tangerg/scope/core/chat"
)

func mapProtocolConverseResponse(model string, output *bedrockruntime.ConverseOutput) (*corechat.Response, error) {
	if output == nil || output.Output == nil {
		return nil, errors.New("bedrock: response has no output")
	}
	messageOutput, ok := output.Output.(*types.ConverseOutputMemberMessage)
	if !ok || messageOutput == nil {
		return nil, errors.New("bedrock: response has no message output")
	}
	parts, err := mapProtocolResponseBlocks(messageOutput.Value.Content)
	if err != nil {
		return nil, err
	}
	modelOutput := &corechat.Output{FinishReason: mapProtocolStopReason(output.StopReason)}
	if len(parts) != 0 {
		message := corechat.NewAssistantMessage(parts...)
		modelOutput.Message = &message
	}
	if modelOutput.FinishReason == corechat.FinishReasonOther {
		modelOutput.Metadata = &corechat.OutputMetadata{}
		if err := modelOutput.Metadata.Extra.Set(chatNativeFinishReasonKey, string(output.StopReason)); err != nil {
			return nil, err
		}
	}
	response := &corechat.Response{
		Output:   modelOutput,
		Metadata: &corechat.ResponseMetadata{Model: model, Usage: mapProtocolUsage(output.Usage)},
	}
	if err := response.Metadata.Extra.Set(ChatResponseExtensionKey, output); err != nil {
		return nil, fmt.Errorf("bedrock: preserve native response: %w", err)
	}
	if err := response.Validate(); err != nil {
		return nil, fmt.Errorf("bedrock: response: %w", err)
	}
	return response, nil
}

func mapProtocolResponseBlocks(blocks []types.ContentBlock) ([]corechat.Part, error) {
	parts := make([]corechat.Part, 0, len(blocks))
	for index := range blocks {
		part, include, err := mapProtocolResponseBlock(blocks[index])
		if err != nil {
			return nil, fmt.Errorf("bedrock: response content[%d]: %w", index, err)
		}
		if include {
			parts = append(parts, part)
		}
	}
	return parts, nil
}

func mapProtocolResponseBlock(block types.ContentBlock) (corechat.Part, bool, error) {
	switch block := block.(type) {
	case *types.ContentBlockMemberText:
		return corechat.NewTextPart(block.Value), block.Value != "", nil
	case *types.ContentBlockMemberImage:
		value, err := bedrockImageToMedia(block.Value)
		if err != nil {
			return corechat.Part{}, false, err
		}
		return corechat.NewMediaPart(value), true, nil
	case *types.ContentBlockMemberAudio:
		value, err := bedrockAudioToMedia(block.Value)
		if err != nil {
			return corechat.Part{}, false, err
		}
		return corechat.NewMediaPart(value), true, nil
	case *types.ContentBlockMemberVideo:
		value, err := bedrockVideoToMedia(block.Value)
		if err != nil {
			return corechat.Part{}, false, err
		}
		return corechat.NewMediaPart(value), true, nil
	case *types.ContentBlockMemberReasoningContent:
		return mapProtocolReasoningContent(block.Value)
	case *types.ContentBlockMemberToolUse:
		part, err := mapProtocolToolUse(block.Value)
		return part, err == nil, err
	default:
		return corechat.Part{}, false, nil
	}
}

func mapProtocolReasoningContent(block types.ReasoningContentBlock) (corechat.Part, bool, error) {
	switch reasoning := block.(type) {
	case *types.ReasoningContentBlockMemberReasoningText:
		if reasoning.Value.Text == nil || reasoning.Value.Signature == nil {
			return corechat.Part{}, false, errors.New("reasoning text lacks text or signature")
		}
		part, err := NewReasoningPart(*reasoning.Value.Text, []byte(*reasoning.Value.Signature))
		return part, err == nil, err
	case *types.ReasoningContentBlockMemberRedactedContent:
		part, err := NewRedactedReasoningPart(reasoning.Value)
		return part, err == nil, err
	default:
		return corechat.Part{}, false, nil
	}
}

func mapProtocolToolUse(value types.ToolUseBlock) (corechat.Part, error) {
	if value.ToolUseId == nil || value.Name == nil {
		return corechat.Part{}, errors.New("tool use lacks ID or name")
	}
	arguments, err := json.Marshal(value.Input)
	if err != nil {
		return corechat.Part{}, fmt.Errorf("tool arguments: %w", err)
	}
	return corechat.NewToolCallPart(corechat.ToolCall{
		ID: *value.ToolUseId, Name: *value.Name, Arguments: string(arguments),
	}), nil
}

func mapProtocolStopReason(reason types.StopReason) corechat.FinishReason {
	switch reason {
	case types.StopReasonEndTurn, types.StopReasonStopSequence:
		return corechat.FinishReasonStop
	// Converse reports two truncations: max_tokens is the budget the caller
	// set, model_context_window_exceeded is the model's own window filling
	// first. Both leave a half-finished output, so both are length.
	case types.StopReasonMaxTokens, types.StopReasonModelContextWindowExceeded:
		return corechat.FinishReasonLength
	case types.StopReasonToolUse:
		return corechat.FinishReasonToolCalls
	case types.StopReasonContentFiltered, types.StopReasonGuardrailIntervened:
		return corechat.FinishReasonContentFilter
	// malformed_model_output and malformed_tool_use are generation faults with
	// no portable match, so they stay other rather than borrowing a reason
	// callers would act on.
	default:
		return corechat.FinishReasonOther
	}
}

func mapProtocolUsage(usage *types.TokenUsage) corechat.Usage {
	if usage == nil {
		return corechat.Usage{}
	}
	result := corechat.Usage{}
	if usage.OutputTokens != nil {
		result.OutputTokens = int64(*usage.OutputTokens)
	}

	var uncached, cacheRead, cacheWrite int64
	if usage.InputTokens != nil {
		uncached = int64(*usage.InputTokens)
	}
	if usage.CacheReadInputTokens != nil {
		cacheRead = int64(*usage.CacheReadInputTokens)
		result.CacheReadInputTokens = &cacheRead
	}
	if usage.CacheWriteInputTokens != nil {
		cacheWrite = int64(*usage.CacheWriteInputTokens)
		result.CacheWriteInputTokens = &cacheWrite
	}
	result.InputTokens = protocolTotalInputTokens(uncached, cacheRead, cacheWrite)
	return result
}

// protocolTotalInputTokens converts Converse's disjoint input counters into the
// total Core reports.
//
// With prompt caching, "the inputTokens field represents only the non-cached
// input tokens", and AWS gives the total as
// inputTokens + cacheReadInputTokens + cacheWriteInputTokens. Core's
// InputTokens is that total, with the cache counts as breakdowns of it, so
// copying the similarly named field both understates the input — usually by the
// whole cached prefix — and puts a breakdown above its own total, which
// [corechat.Usage.Validate] rejects. A cache hit would fail the response.
func protocolTotalInputTokens(uncached, cacheRead, cacheWrite int64) int64 {
	return uncached + cacheRead + cacheWrite
}
