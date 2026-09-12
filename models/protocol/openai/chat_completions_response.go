package openai

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	openaisdk "github.com/openai/openai-go/v3"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
)

func mapCompletion(params *openaisdk.ChatCompletionNewParams, response *openaisdk.ChatCompletion, dialect Dialect) (*corechat.Response, error) {
	if response == nil {
		return nil, errors.New("openai: nil response")
	}
	if len(response.Choices) == 0 {
		return nil, errors.New("openai: response has no choices")
	}
	if len(response.Choices) > 1 {
		return nil, fmt.Errorf("openai: response has %d choices; Core supports one output", len(response.Choices))
	}
	output, err := mapCompletionOutput(params, response.Choices[0], dialect.response)
	if err != nil {
		return nil, fmt.Errorf("openai: output: %w", err)
	}
	mapped := &corechat.Response{
		Output: output,
		Metadata: &corechat.ResponseMetadata{
			ID: response.ID, Model: response.Model, Usage: mapUsage(response.Usage),
		},
	}
	if response.Created > 0 {
		mapped.Metadata.CreatedAt = time.Unix(response.Created, 0).UTC()
	}
	if err := mapped.Metadata.Extra.Set(protocolResponseExtensionKey(dialect.Provider), exactProviderResponse(response.RawJSON(), response)); err != nil {
		return nil, err
	}
	if err := mapped.Validate(); err != nil {
		return nil, fmt.Errorf("openai: mapped response: %w", err)
	}
	return mapped, nil
}

func mapCompletionOutput(params *openaisdk.ChatCompletionNewParams, choice openaisdk.ChatCompletionChoice, dialect responseDialect) (*corechat.Output, error) {
	if choice.Index != 0 {
		return nil, fmt.Errorf("choice index is %d, want 0", choice.Index)
	}
	mapped := &corechat.Output{FinishReason: normalizeFinishReason(choice.FinishReason)}
	message, err := mapCompletionMessage(params, choice.Message, dialect)
	if err != nil {
		return nil, err
	}
	mapped.Message = message
	if choice.Message.Refusal != "" {
		mapped.FinishReason = corechat.FinishReasonRefusal
	}
	return mapped, nil
}

func mapCompletionMessage(params *openaisdk.ChatCompletionNewParams, message openaisdk.ChatCompletionMessage, dialect responseDialect) (*corechat.Message, error) {
	var parts []corechat.Part
	if message.Content != "" {
		part := corechat.NewTextPart(message.Content)
		for index := range message.Annotations {
			citation := message.Annotations[index].URLCitation
			part.Citations = append(part.Citations, corechat.Citation{
				Source: corechat.CitationSource{Kind: corechat.CitationSourceURI, Value: citation.URL},
				Title:  citation.Title,
			})
		}
		parts = append(parts, part)
	}
	if message.Refusal != "" {
		parts = append(parts, corechat.NewRefusalPart(message.Refusal))
	}
	for i := range message.ToolCalls {
		call, err := mapResponseToolCall(message.ToolCalls[i])
		if err != nil {
			return nil, fmt.Errorf("tool_calls[%d]: %w", i, err)
		}
		parts = append(parts, corechat.NewToolCallPart(call))
	}
	if message.Audio.ID != "" || message.Audio.Data != "" {
		audio, err := mapOutputAudio(params, message.Audio)
		if err != nil {
			return nil, err
		}
		if message.Audio.Transcript != "" && message.Content == "" {
			parts = append(parts, corechat.NewTextPart(message.Audio.Transcript))
		}
		parts = append(parts, corechat.NewMediaPart(audio))
	}
	mapped := &corechat.Message{Role: corechat.RoleAssistant, Parts: parts}
	if dialect != nil {
		if err := dialect.FinalizeMessage(message, mapped); err != nil {
			return nil, fmt.Errorf("response dialect: %w", err)
		}
	}
	if len(mapped.Parts) == 0 {
		return nil, nil
	}
	return mapped, nil
}

func mapResponseToolCall(toolCall openaisdk.ChatCompletionMessageToolCallUnion) (corechat.ToolCall, error) {
	switch toolCall.Type {
	case "", "function":
		return corechat.ToolCall{
			ID:        toolCall.ID,
			Name:      toolCall.Function.Name,
			Arguments: toolCall.Function.Arguments,
		}, nil
	case "custom":
		return corechat.ToolCall{
			ID:        toolCall.ID,
			Name:      toolCall.Custom.Name,
			Arguments: toolCall.Custom.Input,
		}, nil
	default:
		return corechat.ToolCall{}, fmt.Errorf("unsupported type %q", toolCall.Type)
	}
}

func mapOutputAudio(params *openaisdk.ChatCompletionNewParams, audio openaisdk.ChatCompletionAudio) (*media.Media, error) {
	format := string(params.Audio.Format)
	if format == "" {
		if audioField, ok := params.ExtraFields()["audio"].(map[string]any); ok {
			format, _ = audioField["format"].(string)
		}
	}
	mimeType := audioMIME(format)
	var mapped *media.Media
	var err error
	if audio.ID != "" {
		mapped, err = media.NewReference(mimeType, audio.ID)
	} else {
		data, decodeErr := base64.StdEncoding.DecodeString(audio.Data)
		if decodeErr != nil {
			return nil, fmt.Errorf("openai: decode output audio: %w", decodeErr)
		}
		mapped, err = media.NewBytes(mimeType, data)
	}
	if err != nil {
		return nil, err
	}
	mapped.ID = audio.ID
	return mapped, nil
}

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
