package openai

import (
	"encoding/json"
	"encoding/json/jsontext"

	openaisdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared/constant"

	corechat "github.com/Tangerg/scope/core/chat"
)

const (
	audioMIMEOpus   = "audio/opus"
	audioMIMEAAC    = "audio/aac"
	audioMIMEL16    = "audio/L16"
	audioMIMEBinary = "audio/octet-stream"
)

const (
	finishReasonStop          = "stop"
	finishReasonLength        = "length"
	finishReasonContentFilter = "content_filter"
)

func audioMIME(format string) string {
	switch format {
	case string(openaisdk.ChatCompletionAudioParamFormatWAV):
		return audioMIMEWAV
	case string(openaisdk.ChatCompletionAudioParamFormatMP3):
		return audioMIMEMPEG
	case string(openaisdk.ChatCompletionAudioParamFormatFLAC):
		return audioMIMEFLAC
	case string(openaisdk.ChatCompletionAudioParamFormatOpus):
		return audioMIMEOpus
	case string(openaisdk.ChatCompletionAudioParamFormatAAC):
		return audioMIMEAAC
	case string(openaisdk.ChatCompletionAudioParamFormatPcm16):
		return audioMIMEL16
	default:
		return audioMIMEBinary
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
