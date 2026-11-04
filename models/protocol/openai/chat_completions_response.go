package openai

import (
	"encoding/json"
	"encoding/json/jsontext"

	"github.com/openai/openai-go/v3/shared/constant"

	corechat "github.com/Tangerg/scope/core/chat"
)

const (
	finishReasonStop          = "stop"
	finishReasonLength        = "length"
	finishReasonContentFilter = "content_filter"
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

func normalizeFinishReason(reason string) corechat.FinishReason {
	switch reason {
	case "":
		return ""
	case finishReasonStop:
		return corechat.FinishReasonStop
	case finishReasonLength:
		return corechat.FinishReasonLength
	case string(constant.ValueOf[constant.ToolCalls]()):
		return corechat.FinishReasonToolCalls
	case finishReasonContentFilter:
		return corechat.FinishReasonContentFilter
	default:
		return corechat.FinishReasonOther
	}
}

func exactProviderResponse(raw string, fallback any) any {
	if jsontext.Value([]byte(raw)).IsValid() {
		return json.RawMessage(raw)
	}
	return fallback
}
