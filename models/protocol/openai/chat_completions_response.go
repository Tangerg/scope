package openai

import (
	"encoding/json"

	openaisdk "github.com/openai/openai-go/v3"

	corechat "github.com/Tangerg/scope/core/chat"
)

func audioMIME(format string) string {
	switch format {
	case "wav":
		return "audio/wav"
	case "mp3":
		return "audio/mpeg"
	case "flac":
		return "audio/flac"
	case "opus":
		return "audio/opus"
	case "aac":
		return "audio/aac"
	case "pcm16":
		return "audio/L16"
	default:
		return "audio/octet-stream"
	}
}

func mapUsage(usage openaisdk.CompletionUsage) *corechat.Usage {
	if usage.RawJSON() != "" {
		if !usage.JSON.PromptTokens.Valid() || !usage.JSON.CompletionTokens.Valid() {
			return nil
		}
	} else if usage.PromptTokens == 0 && usage.CompletionTokens == 0 {
		return nil
	}
	mapped := corechat.Usage{
		InputTokens:  usage.PromptTokens,
		OutputTokens: usage.CompletionTokens,
	}
	if usage.CompletionTokensDetails.JSON.ReasoningTokens.Valid() || usage.CompletionTokensDetails.ReasoningTokens != 0 {
		value := usage.CompletionTokensDetails.ReasoningTokens
		mapped.ReasoningTokens = &value
	}
	if usage.PromptTokensDetails.JSON.CachedTokens.Valid() || usage.PromptTokensDetails.CachedTokens != 0 {
		value := usage.PromptTokensDetails.CachedTokens
		mapped.CacheReadInputTokens = &value
	}
	return &mapped
}

func normalizeFinishReason(reason string) corechat.FinishReason {
	switch reason {
	case "":
		return ""
	case "stop":
		return corechat.FinishReasonStop
	case "length":
		return corechat.FinishReasonLength
	case "tool_calls", "function_call":
		return corechat.FinishReasonToolCalls
	case "content_filter":
		return corechat.FinishReasonContentFilter
	default:
		return corechat.FinishReasonOther
	}
}

func exactProviderResponse(raw string, fallback any) any {
	if json.Valid([]byte(raw)) {
		return json.RawMessage(raw)
	}
	return fallback
}
