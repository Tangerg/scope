package mistral

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
)

const (
	expectedResponseChoices = 1
	firstChoiceIndex        = 0
)

func mapChatCompletion(completion *chatCompletionResponse) (*corechat.Response, error) {
	if completion == nil {
		return nil, errors.New("mistral: nil chat completion response")
	}
	if len(completion.Choices) != expectedResponseChoices {
		return nil, fmt.Errorf("mistral: response has %d choices; Core requires one output", len(completion.Choices))
	}
	response := &corechat.Response{
		Metadata: &corechat.ResponseMetadata{
			ID: completion.ID, Model: completion.Model, Usage: mapMistralUsage(completion.Usage),
		},
	}
	if err := response.Metadata.Extra.Set(responseExtensionKey, completion); err != nil {
		return nil, err
	}
	wireChoice := completion.Choices[0]
	if wireChoice.Index != firstChoiceIndex {
		return nil, fmt.Errorf("mistral: choice index is %d, want %d", wireChoice.Index, firstChoiceIndex)
	}
	parts, err := mapMistralContent(wireChoice.Message.Content)
	if err != nil {
		return nil, fmt.Errorf("mistral: output message content: %w", err)
	}
	toolParts, err := mapMistralToolCalls(wireChoice.Message.ToolCalls)
	if err != nil {
		return nil, fmt.Errorf("mistral: output message tool calls: %w", err)
	}
	parts = append(parts, toolParts...)
	finish := normalizeMistralFinishReason(wireChoice.FinishReason)
	nativeFinish, err := mapMistralNativeFinishReason(wireChoice.FinishReason, finish)
	if err != nil {
		return nil, err
	}
	response.Output = &corechat.Output{FinishReason: finish, Metadata: nativeFinish}
	if response.Output.FinishReason == corechat.FinishReasonOther {
		response.Output.Metadata = &corechat.OutputMetadata{}
		if err := response.Output.Metadata.Extra.Set(nativeFinishReasonKey, wireChoice.FinishReason); err != nil {
			return nil, err
		}
	}
	if len(parts) > 0 {
		response.Output.Message = &corechat.Message{Role: corechat.RoleAssistant, Parts: parts}
	}
	if err := response.Validate(); err != nil {
		return nil, fmt.Errorf("mistral: mapped chat completion: %w", err)
	}
	return response, nil
}

func mapMistralContent(raw json.RawMessage) ([]corechat.Part, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return nil, err
		}
		if text == "" {
			return nil, nil
		}
		return []corechat.Part{corechat.NewTextPart(text)}, nil
	}
	var chunks []json.RawMessage
	if err := json.Unmarshal(trimmed, &chunks); err != nil {
		return nil, err
	}
	parts := make([]corechat.Part, 0, len(chunks))
	for index := range chunks {
		citations, reference, err := mapMistralReferenceChunk(chunks[index])
		if err != nil {
			return nil, fmt.Errorf("chunk[%d]: %w", index, err)
		}
		if reference {
			attachMistralCitations(parts, citations)
			continue
		}
		part, include, err := mapMistralContentChunk(chunks[index])
		if err != nil {
			return nil, fmt.Errorf("chunk[%d]: %w", index, err)
		}
		if include {
			parts = append(parts, part)
		}
	}
	return parts, nil
}

func mapMistralContentChunk(raw json.RawMessage) (corechat.Part, bool, error) {
	var discriminator struct {
		Type contentType `json:"type"`
	}
	if err := json.Unmarshal(raw, &discriminator); err != nil {
		return corechat.Part{}, false, err
	}
	switch discriminator.Type {
	case contentTypeText:
		var chunk textChunk
		if err := json.Unmarshal(raw, &chunk); err != nil {
			return corechat.Part{}, false, err
		}
		return corechat.NewTextPart(chunk.Text), chunk.Text != "", nil
	case contentTypeThinking:
		part, err := mapMistralThinkingChunk(raw)
		return part, err == nil, err
	case contentTypeImageURL:
		part, err := mapMistralImageChunk(raw)
		return part, err == nil, err
	default:
		return corechat.Part{}, false, fmt.Errorf("unsupported content type %q", discriminator.Type)
	}
}

func mapMistralContentDeltas(raw json.RawMessage) ([]corechat.PartDelta, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return nil, err
		}
		if text == "" {
			return nil, nil
		}
		return []corechat.PartDelta{corechat.NewTextDelta(text)}, nil
	}
	var chunks []json.RawMessage
	if err := json.Unmarshal(trimmed, &chunks); err != nil {
		return nil, err
	}
	deltas := make([]corechat.PartDelta, 0, len(chunks))
	for index := range chunks {
		citations, reference, err := mapMistralReferenceChunk(chunks[index])
		if err != nil {
			return nil, fmt.Errorf("chunk[%d]: %w", index, err)
		}
		if reference {
			for citationIndex := range citations {
				deltas = append(deltas, corechat.NewCitationDelta(citations[citationIndex]))
			}
			continue
		}
		part, include, err := mapMistralContentChunk(chunks[index])
		if err != nil {
			return nil, fmt.Errorf("chunk[%d]: %w", index, err)
		}
		if !include {
			continue
		}
		switch part.Kind {
		case corechat.PartText:
			deltas = append(deltas, corechat.NewTextDelta(part.Text))
		case corechat.PartReasoning:
			deltas = append(deltas, corechat.NewReasoningDelta(part.Text, part.ReasoningState))
		case corechat.PartMedia:
			deltas = append(deltas, corechat.NewMediaDelta(part.Media))
		default:
			return nil, fmt.Errorf("chunk[%d]: unsupported stream part %q", index, part.Kind)
		}
	}
	return deltas, nil
}

func mapMistralReferenceChunk(raw json.RawMessage) ([]corechat.Citation, bool, error) {
	var chunk struct {
		Type         contentType       `json:"type"`
		ReferenceIDs []json.RawMessage `json:"reference_ids"`
	}
	if err := json.Unmarshal(raw, &chunk); err != nil {
		return nil, false, err
	}
	if chunk.Type != contentTypeReference && chunk.Type != contentTypeToolReference {
		return nil, false, nil
	}
	citations := make([]corechat.Citation, 0, len(chunk.ReferenceIDs))
	for index := range chunk.ReferenceIDs {
		reference, err := mistralReferenceID(chunk.ReferenceIDs[index])
		if err != nil {
			return nil, true, fmt.Errorf("reference_ids[%d]: %w", index, err)
		}
		citations = append(citations, corechat.Citation{
			Source: corechat.CitationSource{Kind: corechat.CitationSourceReference, Value: reference},
		})
	}
	return citations, true, nil
}

func mistralReferenceID(raw json.RawMessage) (string, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if text == "" {
			return "", errors.New("reference ID is empty")
		}
		return text, nil
	}
	trimmed := bytes.TrimSpace(raw)
	var number json.Number
	if err := json.Unmarshal(trimmed, &number); err != nil || number.String() == "" {
		return "", errors.New("reference ID must be a string or number")
	}
	return number.String(), nil
}

func attachMistralCitations(parts []corechat.Part, citations []corechat.Citation) {
	if len(citations) == 0 {
		return
	}
	for index := len(parts) - 1; index >= 0; index-- {
		if parts[index].Kind == corechat.PartText {
			parts[index].Citations = append(parts[index].Citations, citations...)
			return
		}
	}
}

func mapMistralThinkingChunk(raw json.RawMessage) (corechat.Part, error) {
	var chunk struct {
		Thinking []json.RawMessage `json:"thinking"`
	}
	if err := json.Unmarshal(raw, &chunk); err != nil {
		return corechat.Part{}, err
	}
	var reasoning strings.Builder
	for nestedIndex := range chunk.Thinking {
		var nested textChunk
		if err := json.Unmarshal(chunk.Thinking[nestedIndex], &nested); err == nil && nested.Type == contentTypeText {
			reasoning.WriteString(nested.Text)
		}
	}
	frame, err := encodeThinkingFrame(raw)
	if err != nil {
		return corechat.Part{}, err
	}
	return corechat.NewReasoningPart(reasoning.String(), frame), nil
}

func mapMistralImageChunk(raw json.RawMessage) (corechat.Part, error) {
	var chunk imageURLChunk
	if err := json.Unmarshal(raw, &chunk); err != nil {
		return corechat.Part{}, err
	}
	image, err := media.NewURI("image/*", string(chunk.ImageURL))
	if err != nil {
		return corechat.Part{}, err
	}
	return corechat.NewMediaPart(image), nil
}

func mapMistralToolCalls(calls []chatToolCall) ([]corechat.Part, error) {
	parts := make([]corechat.Part, 0, len(calls))
	for index := range calls {
		call := calls[index]
		if call.ID == "" {
			return nil, fmt.Errorf("tool call %d has no ID", index)
		}
		if call.Function.Name == "" {
			return nil, fmt.Errorf("tool call %d has no function name", index)
		}
		arguments, err := mistralToolArguments(call.Function.Arguments)
		if err != nil {
			return nil, fmt.Errorf("tool call %d arguments: %w", index, err)
		}
		parts = append(parts, corechat.NewToolCallPart(corechat.ToolCall{
			ID: call.ID, Name: call.Function.Name, Arguments: arguments,
		}))
	}
	return parts, nil
}

func mistralToolArguments(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", nil
	}
	if trimmed[0] == '"' {
		var value string
		if err := json.Unmarshal(trimmed, &value); err != nil {
			return "", err
		}
		return value, nil
	}
	if !json.Valid(trimmed) {
		return "", errors.New("invalid JSON")
	}
	return string(trimmed), nil
}

func mapMistralUsage(usage chatUsage) corechat.Usage {
	mapped := corechat.Usage{InputTokens: usage.PromptTokens, OutputTokens: usage.CompletionTokens}
	cached := usage.NumCachedTokens
	if usage.PromptTokensDetails != nil && usage.PromptTokensDetails.CachedTokens != 0 {
		cached = usage.PromptTokensDetails.CachedTokens
	}
	if cached != 0 {
		mapped.CacheReadInputTokens = &cached
	}
	return mapped
}

// normalizeMistralFinishReason maps the five values Mistral's client declares —
// stop, length, model_length, error and tool_calls — and answers anything else
// with Other, because that client types the field as those literals or an
// unrecognized string, so a value outside the set is something this adapter has
// not seen rather than something it failed to classify.
//
// error and an unrecognized value both land on Other, which is what Core
// reserves for a known terminal state with no portable match. Neither is
// silently lost: mapMistralNativeFinishReason keeps the provider's own word for
// it on the output, the way this adapter's siblings do, so a caller can tell an
// errored generation from a provider iteration limit.
func normalizeMistralFinishReason(reason finishReason) corechat.FinishReason {
	switch reason {
	case "":
		return ""
	case finishReasonStop:
		return corechat.FinishReasonStop
	case finishReasonLength, finishReasonModelLength:
		return corechat.FinishReasonLength
	case finishReasonToolCalls:
		return corechat.FinishReasonToolCalls
	case finishReasonError:
		return corechat.FinishReasonOther
	default:
		return corechat.FinishReasonOther
	}
}

// mapMistralNativeFinishReason records the provider's own finish reason
// whenever the portable one is Other, so the distinction Other erases stays
// available.
func mapMistralNativeFinishReason(
	reason finishReason,
	mapped corechat.FinishReason,
) (*corechat.OutputMetadata, error) {
	if mapped != corechat.FinishReasonOther {
		return nil, nil
	}
	outputMetadata := &corechat.OutputMetadata{}
	if err := outputMetadata.Extra.Set(nativeFinishReasonKey, string(reason)); err != nil {
		return nil, fmt.Errorf("mistral: record native finish reason: %w", err)
	}
	return outputMetadata, nil
}
