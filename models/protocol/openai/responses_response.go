package openai

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/openai/openai-go/v3/responses"

	corechat "github.com/Tangerg/scope/core/chat"
)

func responsesTerminalDelta(response *responses.Response, extensionKey string) (*corechat.ResponseDelta, error) {
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
	if err := metadata.Extra.Set(extensionKey, json.RawMessage(response.RawJSON())); err != nil {
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
		return "", fmt.Errorf("%w: openai responses: failed: %s: %s", corechat.ErrDefiniteFailure, response.Error.Code, response.Error.Message)
	case responses.ResponseStatusCancelled:
		return "", fmt.Errorf("%w: openai responses: response was canceled", corechat.ErrDefiniteFailure)
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
	if usage.OutputTokensDetails.JSON.ReasoningTokens.Valid() || usage.OutputTokensDetails.ReasoningTokens != 0 {
		value := usage.OutputTokensDetails.ReasoningTokens
		result.ReasoningTokens = &value
	}
	if usage.InputTokensDetails.JSON.CachedTokens.Valid() || usage.InputTokensDetails.CachedTokens != 0 {
		value := usage.InputTokensDetails.CachedTokens
		result.CacheReadInputTokens = &value
	}
	if usage.InputTokensDetails.JSON.CacheWriteTokens.Valid() || usage.InputTokensDetails.CacheWriteTokens != 0 {
		value := usage.InputTokensDetails.CacheWriteTokens
		result.CacheWriteInputTokens = &value
	}
	return &result
}
