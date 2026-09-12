package openai

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/openai/openai-go/v3/responses"

	corechat "github.com/Tangerg/scope/core/chat"
)

func mapResponsesResponse(response *responses.Response) (*corechat.Response, error) {
	terminal, err := responsesTerminalDelta(response)
	if err != nil {
		return nil, err
	}
	parts, err := responsesOutputParts(response.Output)
	if err != nil {
		return nil, err
	}
	output := &corechat.Output{FinishReason: terminal.FinishReason}
	if len(parts) != 0 {
		message := corechat.NewAssistantMessage(parts...)
		output.Message = &message
	}
	mapped := &corechat.Response{Output: output, Metadata: terminal.Metadata}
	if err := mapped.Validate(); err != nil {
		return nil, fmt.Errorf("openai responses: response: %w", err)
	}
	return mapped, nil
}

func responsesOutputParts(output []responses.ResponseOutputItemUnion) ([]corechat.Part, error) {
	parts := make([]corechat.Part, 0, len(output))
	for index := range output {
		item := output[index]
		switch item.Type {
		case responsesItemTypeMessage:
			message := item.AsMessage()
			for contentIndex := range message.Content {
				content := message.Content[contentIndex]
				switch content.Type {
				case responsesContentTypeText:
					if content.Text == "" {
						continue
					}
					part := corechat.NewTextPart(content.Text)
					for annotationIndex := range content.Annotations {
						citation, include, mapErr := responsesCitation(content.Annotations[annotationIndex])
						if mapErr != nil {
							return nil, fmt.Errorf("openai responses: output[%d].content[%d].annotations[%d]: %w", index, contentIndex, annotationIndex, mapErr)
						}
						if include {
							part.Citations = append(part.Citations, citation)
						}
					}
					parts = append(parts, part)
				case responsesContentTypeRefusal:
					if content.Refusal != "" {
						parts = append(parts, corechat.NewRefusalPart(content.Refusal))
					}
				}
			}
		case responsesItemTypeReasoning:
			reasoning := item.AsReasoning()
			text := joinResponsesReasoning(reasoning)
			signature, encodeErr := encodeResponsesReasoningFrame(reasoning.ToParam())
			if encodeErr != nil {
				return nil, fmt.Errorf("openai responses: output[%d] reasoning: %w", index, encodeErr)
			}
			parts = append(parts, corechat.NewReasoningPart(text, signature))
		case responsesItemTypeFunctionCall:
			call := item.AsFunctionCall()
			id := call.CallID
			if id == "" {
				id = call.ID
			}
			if id == "" || call.Name == "" {
				return nil, fmt.Errorf("openai responses: output[%d] function call lacks ID or name", index)
			}
			parts = append(parts, corechat.NewToolCallPart(corechat.ToolCall{ID: id, Name: call.Name, Arguments: call.Arguments}))
		}
	}
	return parts, nil
}

func responsesCitation(annotation responses.ResponseOutputTextAnnotationUnion) (corechat.Citation, bool, error) {
	switch typed := annotation.AsAny().(type) {
	case responses.ResponseOutputTextAnnotationURLCitation:
		return corechat.Citation{
			Source: corechat.CitationSource{Kind: corechat.CitationSourceURI, Value: typed.URL},
			Title:  typed.Title,
		}, true, nil
	case responses.ResponseOutputTextAnnotationFileCitation:
		return corechat.Citation{
			Source: corechat.CitationSource{Kind: corechat.CitationSourceReference, Value: typed.FileID},
			Title:  typed.Filename,
		}, true, nil
	case responses.ResponseOutputTextAnnotationContainerFileCitation:
		return corechat.Citation{
			Source: corechat.CitationSource{Kind: corechat.CitationSourceReference, Value: typed.FileID},
			Title:  typed.Filename,
		}, true, nil
	case responses.ResponseOutputTextAnnotationFilePath:
		return corechat.Citation{
			Source: corechat.CitationSource{Kind: corechat.CitationSourceReference, Value: typed.FileID},
		}, true, nil
	case nil:
		return corechat.Citation{}, false, nil
	default:
		return corechat.Citation{}, false, fmt.Errorf("unsupported annotation %T", typed)
	}
}

func joinResponsesReasoning(reasoning responses.ResponseReasoningItem) string {
	var text strings.Builder
	if len(reasoning.Content) != 0 {
		for _, content := range reasoning.Content {
			text.WriteString(content.Text)
		}
	} else {
		for _, summary := range reasoning.Summary {
			text.WriteString(summary.Text)
		}
	}
	return text.String()
}

func responsesTerminalDelta(response *responses.Response) (*corechat.ResponseDelta, error) {
	if response == nil {
		return nil, fmt.Errorf("%w: openai responses: nil response", corechat.ErrInvalidResponse)
	}
	finishReason, err := responsesFinishReason(response)
	if err != nil {
		return nil, err
	}
	metadata := &corechat.ResponseMetadata{
		ID: response.ID, Model: string(response.Model), Usage: responsesUsage(response.Usage),
	}
	if response.CreatedAt > 0 {
		metadata.CreatedAt = time.Unix(int64(response.CreatedAt), 0).UTC()
	}
	if err := metadata.Extra.Set(ResponsesResponseExtensionKey, json.RawMessage(response.RawJSON())); err != nil {
		return nil, fmt.Errorf("openai responses: preserve native response: %w", err)
	}
	delta := &corechat.ResponseDelta{FinishReason: finishReason, Metadata: metadata}
	if err := delta.Validate(); err != nil {
		return nil, err
	}
	return delta, nil
}

func responsesFinishReason(response *responses.Response) (corechat.FinishReason, error) {
	switch response.Status {
	case responses.ResponseStatusFailed:
		return "", fmt.Errorf("openai responses: failed: %s: %s", response.Error.Code, response.Error.Message)
	case responses.ResponseStatusCancelled:
		return "", fmt.Errorf("openai responses: response was canceled")
	case responses.ResponseStatusIncomplete:
		switch response.IncompleteDetails.Reason {
		case responsesIncompleteMaxTokens:
			return corechat.FinishReasonLength, nil
		case responsesIncompleteFiltered:
			return corechat.FinishReasonContentFilter, nil
		default:
			return corechat.FinishReasonOther, nil
		}
	case responses.ResponseStatusCompleted:
	default:
		return "", fmt.Errorf("%w: openai responses: nonterminal or unknown status %q", corechat.ErrInvalidResponse, response.Status)
	}
	finishReason := corechat.FinishReasonStop
	for _, item := range response.Output {
		switch item.Type {
		case responsesItemTypeFunctionCall:
			finishReason = corechat.FinishReasonToolCalls
		case responsesItemTypeMessage:
			for _, content := range item.AsMessage().Content {
				if content.Type == responsesContentTypeRefusal {
					return corechat.FinishReasonRefusal, nil
				}
			}
		}
	}
	return finishReason, nil
}

func responsesUsage(usage responses.ResponseUsage) *corechat.Usage {
	if usage.RawJSON() != "" {
		if !usage.JSON.InputTokens.Valid() || !usage.JSON.OutputTokens.Valid() {
			return nil
		}
	} else if usage.InputTokens == 0 && usage.OutputTokens == 0 {
		return nil
	}
	result := corechat.Usage{InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens}
	if usage.OutputTokensDetails.ReasoningTokens > 0 {
		value := usage.OutputTokensDetails.ReasoningTokens
		result.ReasoningTokens = &value
	}
	if usage.InputTokensDetails.CachedTokens > 0 {
		value := usage.InputTokensDetails.CachedTokens
		result.CacheReadInputTokens = &value
	}
	return &result
}
