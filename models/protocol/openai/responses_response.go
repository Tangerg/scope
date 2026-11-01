package openai

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/openai/openai-go/v3/responses"

	corechat "github.com/Tangerg/scope/core/chat"
)

func responsesTerminalDelta(response *responses.Response, extensionKey string, identity streamIdentity) (*corechat.ResponseDelta, error) {
	if response == nil {
		return nil, fmt.Errorf("%w: openai responses: nil response", corechat.ErrInvalidResponse)
	}
	finishReason, err := responsesFinishReason(response)
	if err != nil {
		return nil, err
	}
	metadata := identity.metadata()
	metadata.Usage, err = responsesUsage(response.Usage)
	if err != nil {
		return nil, err
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
		return "", &corechat.CallFailure{Kind: corechat.CallFailureFailed, Cause: fmt.Errorf("openai responses: failed: %s: %s", response.Error.Code, response.Error.Message)}
	case responses.ResponseStatusCancelled:
		return "", &corechat.CallFailure{Kind: corechat.CallFailureFailed, Cause: errors.New("openai responses: response was canceled")}
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
