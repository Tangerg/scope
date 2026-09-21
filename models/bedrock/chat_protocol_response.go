package bedrock

import (
	jsonv2 "encoding/json/v2"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	corechat "github.com/Tangerg/scope/core/chat"
)

func marshalProtocolJSON(value any) ([]byte, error) {
	return jsonv2.Marshal(value, jsonv2.Deterministic(true),
		jsonv2.WithMarshalers(jsonv2.MarshalFunc(document.Interface.MarshalSmithyDocument)))
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

func mapProtocolUsage(usage *types.TokenUsage) *corechat.Usage {
	if usage == nil || usage.InputTokens == nil || usage.OutputTokens == nil {
		return nil
	}
	result := corechat.Usage{OutputTokens: int64(*usage.OutputTokens)}

	uncached := int64(*usage.InputTokens)
	var cacheRead, cacheWrite int64
	if usage.CacheReadInputTokens != nil {
		cacheRead = int64(*usage.CacheReadInputTokens)
		result.CacheReadInputTokens = &cacheRead
	}
	if usage.CacheWriteInputTokens != nil {
		cacheWrite = int64(*usage.CacheWriteInputTokens)
		result.CacheWriteInputTokens = &cacheWrite
	}
	result.InputTokens = protocolTotalInputTokens(uncached, cacheRead, cacheWrite)
	return &result
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
