package openai

import (
	jsonv2 "encoding/json/v2"
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
	kind string
	text []byte
}

func (r responsesContentEvidence) matches(content responses.ResponseOutputMessageContentUnion) bool {
	if r.kind != content.Type {
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

const responsesMessageIdentityKey = "openai/responses_message_identity"

// The identity keeps independently phased assistant messages separate while
// Core owns their text. Replay reads this one projection instead of native text.
type responsesMessageIdentity struct {
	ItemID string                               `json:"item_id"`
	Phase  responses.ResponseOutputMessagePhase `json:"phase,omitzero"`
}

func (r responsesMessageIdentity) validate() error {
	if r.ItemID == "" {
		return fmt.Errorf("%w: openai responses: message identity is required", corechat.ErrInvalidResponse)
	}
	switch r.Phase {
	case "", responses.ResponseOutputMessagePhaseCommentary, responses.ResponseOutputMessagePhaseFinalAnswer:
		return nil
	default:
		return fmt.Errorf("%w: openai responses: unknown assistant phase %q", corechat.ErrInvalidResponse, r.Phase)
	}
}

func (r *responsesMessageIdentity) UnmarshalJSON(data []byte) error {
	type wireIdentity responsesMessageIdentity
	var decoded wireIdentity
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return err
	}
	identity := responsesMessageIdentity(decoded)
	if err := identity.validate(); err != nil {
		return err
	}
	*r = identity
	return nil
}

type responsesStreamContent struct {
	segments map[responsesContentPosition]responsesContentEvidence
	messages map[int64]responsesMessageIdentity
}

func (r *responsesStreamContent) bindMessage(index int64, message responses.ResponseOutputMessage) error {
	identity := responsesMessageIdentity{ItemID: message.ID, Phase: message.Phase}
	if err := identity.validate(); err != nil {
		return err
	}
	if index < 0 {
		return fmt.Errorf("%w: openai responses: message output index is negative", corechat.ErrInvalidResponse)
	}
	if previous, exists := r.messages[index]; exists && previous != identity {
		return fmt.Errorf("%w: openai responses: message identity or phase changed at output %d", corechat.ErrInvalidResponse, index)
	}
	for otherIndex, other := range r.messages {
		if otherIndex != index && other.ItemID == identity.ItemID {
			return fmt.Errorf("%w: openai responses: message identity reused", corechat.ErrInvalidResponse)
		}
	}
	if r.messages == nil {
		r.messages = make(map[int64]responsesMessageIdentity)
	}
	r.messages[index] = identity
	return nil
}

func (r *responsesStreamContent) startMessage(event responses.ResponseOutputItemAddedEvent) error {
	if !event.JSON.OutputIndex.Valid() {
		return fmt.Errorf("%w: openai responses: message requires an output index", corechat.ErrInvalidResponse)
	}
	return r.bindMessage(event.OutputIndex, event.Item.AsMessage())
}

func (r *responsesStreamContent) finishMessage(event responses.ResponseOutputItemDoneEvent) error {
	if !event.JSON.OutputIndex.Valid() {
		return fmt.Errorf("%w: openai responses: message completion requires an output index", corechat.ErrInvalidResponse)
	}
	message := event.Item.AsMessage()
	if err := r.bindMessage(event.OutputIndex, message); err != nil {
		return err
	}
	return r.validateCompletedMessage(event.OutputIndex, message)
}

func (r *responsesStreamContent) addText(event responses.ResponseTextDeltaEvent) (*corechat.PartDelta, error) {
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
	if err := part.Metadata.Set(responsesMessageIdentityKey, r.messages[event.OutputIndex]); err != nil {
		return nil, err
	}
	return &part, nil
}

func (r *responsesStreamContent) addRefusal(event responses.ResponseRefusalDeltaEvent) (*corechat.PartDelta, error) {
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
	if err := part.Metadata.Set(responsesMessageIdentityKey, r.messages[event.OutputIndex]); err != nil {
		return nil, err
	}
	return &part, nil
}

func (r *responsesStreamContent) append(position responsesContentPosition, itemID, kind, text string) error {
	if position.output < 0 || position.content < 0 || itemID == "" {
		return fmt.Errorf("%w: openai responses: content delta requires nonnegative indices and an item ID", corechat.ErrInvalidResponse)
	}
	if identity, exists := r.messages[position.output]; exists {
		if identity.ItemID != itemID {
			return fmt.Errorf("%w: openai responses: message identity changed", corechat.ErrInvalidResponse)
		}
	} else if err := r.bindMessage(position.output, responses.ResponseOutputMessage{ID: itemID}); err != nil {
		return err
	}
	if r.segments == nil {
		r.segments = make(map[responsesContentPosition]responsesContentEvidence)
	}
	evidence, exists := r.segments[position]
	if exists && evidence.kind != kind {
		return fmt.Errorf("%w: openai responses: content delta changed kind at output %d content %d", corechat.ErrInvalidResponse, position.output, position.content)
	}
	evidence.kind = kind
	evidence.text = append(evidence.text, text...)
	r.segments[position] = evidence
	return nil
}

func (r *responsesStreamContent) validateTerminal(response responses.Response) error {
	for index, identity := range r.messages {
		if index >= int64(len(response.Output)) || response.Output[index].Type != responsesItemTypeMessage {
			return fmt.Errorf("%w: openai responses: terminal response omitted message at index %d", corechat.ErrInvalidResponse, index)
		}
		message := response.Output[index].AsMessage()
		if identity != (responsesMessageIdentity{ItemID: message.ID, Phase: message.Phase}) {
			return fmt.Errorf("%w: openai responses: terminal response changed message identity or phase", corechat.ErrInvalidResponse)
		}
	}
	for position, evidence := range r.segments {
		if position.output >= int64(len(response.Output)) || response.Output[position.output].Type != responsesItemTypeMessage {
			return fmt.Errorf("%w: openai responses: terminal response omitted message at index %d", corechat.ErrInvalidResponse, position.output)
		}
		message := response.Output[position.output].AsMessage()
		if position.content >= int64(len(message.Content)) || !evidence.matches(message.Content[position.content]) {
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

func (r *responsesStreamContent) validateCompletedMessage(index int64, message responses.ResponseOutputMessage) error {
	for contentIndex, content := range message.Content {
		if content.Type != responsesContentTypeText && content.Type != responsesContentTypeRefusal {
			continue
		}
		position := responsesContentPosition{output: index, content: int64(contentIndex)}
		evidence, exists := r.segments[position]
		if !exists {
			evidence = responsesContentEvidence{kind: content.Type}
		}
		if !evidence.matches(content) {
			return fmt.Errorf("%w: openai responses: completed response introduced content at output %d content %d", corechat.ErrInvalidResponse, index, contentIndex)
		}
	}
	return nil
}
