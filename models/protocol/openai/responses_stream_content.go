package openai

import (
	"fmt"

	"github.com/openai/openai-go/v3/responses"

	corechat "github.com/Tangerg/scope/core/chat"
)

type responsesContentPosition struct {
	output  int64
	content int64
}

// Content is retained only to verify the native terminal projection; Core's
// accumulator remains the sole owner of the assembled response.
type responsesContentEvidence struct {
	itemID string
	kind   string
	text   []byte
}

func (r responsesContentEvidence) matches(itemID string, content responses.ResponseOutputMessageContentUnion) bool {
	if r.itemID != itemID || r.kind != content.Type {
		return false
	}
	switch content.Type {
	case responsesContentTypeText:
		return content.JSON.Text.Valid() && string(r.text) == content.Text
	case responsesContentTypeRefusal:
		return content.JSON.Refusal.Valid() && string(r.text) == content.Refusal
	default:
		return false
	}
}

type responsesStreamContent map[responsesContentPosition]responsesContentEvidence

func (r responsesStreamContent) addText(event responses.ResponseTextDeltaEvent) (*corechat.PartDelta, error) {
	if !event.JSON.OutputIndex.Valid() || !event.JSON.ContentIndex.Valid() || !event.JSON.Delta.Valid() {
		return nil, fmt.Errorf("%w: openai responses: text delta requires output index, content index, and delta", corechat.ErrInvalidResponse)
	}
	position := responsesContentPosition{output: event.OutputIndex, content: event.ContentIndex}
	if err := r.append(position, event.ItemID, responsesContentTypeText, event.Delta); err != nil {
		return nil, err
	}
	if event.Delta == "" {
		return nil, nil
	}
	part := corechat.NewTextDelta(event.Delta)
	return &part, nil
}

func (r responsesStreamContent) addRefusal(event responses.ResponseRefusalDeltaEvent) (*corechat.PartDelta, error) {
	if !event.JSON.OutputIndex.Valid() || !event.JSON.ContentIndex.Valid() || !event.JSON.Delta.Valid() {
		return nil, fmt.Errorf("%w: openai responses: refusal delta requires output index, content index, and delta", corechat.ErrInvalidResponse)
	}
	position := responsesContentPosition{output: event.OutputIndex, content: event.ContentIndex}
	if err := r.append(position, event.ItemID, responsesContentTypeRefusal, event.Delta); err != nil {
		return nil, err
	}
	if event.Delta == "" {
		return nil, nil
	}
	part := corechat.NewRefusalDelta(event.Delta)
	return &part, nil
}

func (r responsesStreamContent) append(position responsesContentPosition, itemID, kind, text string) error {
	if position.output < 0 || position.content < 0 || itemID == "" {
		return fmt.Errorf("%w: openai responses: content delta requires nonnegative indices and an item ID", corechat.ErrInvalidResponse)
	}
	for otherPosition, other := range r {
		if (otherPosition.output == position.output) != (other.itemID == itemID) {
			return fmt.Errorf("%w: openai responses: content delta changed or reused item identity %q at index %d", corechat.ErrInvalidResponse, itemID, position.output)
		}
	}
	evidence, exists := r[position]
	if exists && evidence.kind != kind {
		return fmt.Errorf("%w: openai responses: content delta changed kind at output %d content %d", corechat.ErrInvalidResponse, position.output, position.content)
	}
	evidence.itemID = itemID
	evidence.kind = kind
	evidence.text = append(evidence.text, text...)
	r[position] = evidence
	return nil
}

func (r responsesStreamContent) validateTerminal(response responses.Response) error {
	for position, evidence := range r {
		if position.output >= int64(len(response.Output)) || response.Output[position.output].Type != responsesItemTypeMessage {
			return fmt.Errorf("%w: openai responses: terminal response omitted message at index %d", corechat.ErrInvalidResponse, position.output)
		}
		message := response.Output[position.output].AsMessage()
		if position.content >= int64(len(message.Content)) || !evidence.matches(message.ID, message.Content[position.content]) {
			return fmt.Errorf("%w: openai responses: terminal response changed content at output %d content %d", corechat.ErrInvalidResponse, position.output, position.content)
		}
	}
	if response.Status != responses.ResponseStatusCompleted {
		return nil
	}
	for index, item := range response.Output {
		if item.Type != responsesItemTypeMessage {
			continue
		}
		if err := r.validateCompletedMessage(int64(index), item.AsMessage()); err != nil {
			return err
		}
	}
	return nil
}

func (r responsesStreamContent) validateCompletedMessage(index int64, message responses.ResponseOutputMessage) error {
	for contentIndex, content := range message.Content {
		if content.Type != responsesContentTypeText && content.Type != responsesContentTypeRefusal {
			continue
		}
		position := responsesContentPosition{output: index, content: int64(contentIndex)}
		evidence, exists := r[position]
		if !exists {
			evidence = responsesContentEvidence{itemID: message.ID, kind: content.Type}
		}
		if !evidence.matches(message.ID, content) {
			return fmt.Errorf("%w: openai responses: completed response introduced content at output %d content %d", corechat.ErrInvalidResponse, index, contentIndex)
		}
	}
	return nil
}
