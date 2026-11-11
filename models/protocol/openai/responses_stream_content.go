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
	messages map[int64]responsesMessageState
}

type responsesMessageCompletion struct {
	contentCount int
	status       *responses.ResponseOutputMessageStatus
}

type responsesMessageState struct {
	identity   responsesMessageIdentity
	completion *responsesMessageCompletion
	segments   map[int64]responsesContentEvidence
}

func (r responsesMessageState) requireOpen() error {
	if r.completion != nil {
		return fmt.Errorf("%w: openai responses: message %q is already complete", corechat.ErrInvalidResponse, r.identity.ItemID)
	}
	return nil
}

func (r responsesMessageState) validateTerminal(message responses.ResponseOutputMessage) error {
	if r.identity != (responsesMessageIdentity{ItemID: message.ID, Phase: message.Phase}) {
		return fmt.Errorf("%w: openai responses: terminal response changed message identity or phase", corechat.ErrInvalidResponse)
	}
	if r.completion != nil && (r.completion.contentCount != len(message.Content) || r.completion.status != nil && *r.completion.status != message.Status) {
		return fmt.Errorf("%w: openai responses: terminal response changed completed message %q", corechat.ErrInvalidResponse, r.identity.ItemID)
	}
	for index, evidence := range r.segments {
		if index >= int64(len(message.Content)) || !evidence.matches(message.Content[index]) {
			return fmt.Errorf("%w: openai responses: message %q omitted or changed content at index %d", corechat.ErrInvalidResponse, r.identity.ItemID, index)
		}
	}
	return nil
}

func (r responsesMessageState) validateCompletedContent(message responses.ResponseOutputMessage) error {
	for index, content := range message.Content {
		if content.Type != responsesContentTypeText && content.Type != responsesContentTypeRefusal {
			continue
		}
		evidence, exists := r.segments[int64(index)]
		if !exists {
			evidence = responsesContentEvidence{kind: content.Type}
		}
		if !evidence.matches(content) {
			return fmt.Errorf("%w: openai responses: completed message %q introduced content at index %d", corechat.ErrInvalidResponse, message.ID, index)
		}
	}
	return nil
}

func (r *responsesMessageState) append(index int64, kind, text string) error {
	if err := r.requireOpen(); err != nil {
		return err
	}
	evidence, exists := r.segments[index]
	if exists && evidence.kind != kind {
		return fmt.Errorf("%w: openai responses: message %q changed content kind at index %d", corechat.ErrInvalidResponse, r.identity.ItemID, index)
	}
	evidence.kind = kind
	evidence.text = append(evidence.text, text...)
	if r.segments == nil {
		r.segments = make(map[int64]responsesContentEvidence)
	}
	r.segments[index] = evidence
	return nil
}

func (r *responsesMessageState) finish(message responses.ResponseOutputMessage) error {
	if err := r.requireOpen(); err != nil {
		return err
	}
	if message.JSON.Status.Valid() && message.Status != responses.ResponseOutputMessageStatusCompleted && message.Status != responses.ResponseOutputMessageStatusIncomplete {
		return fmt.Errorf("%w: openai responses: completed message %q has nonterminal or unknown status %q", corechat.ErrInvalidResponse, message.ID, message.Status)
	}
	if err := r.validateTerminal(message); err != nil {
		return err
	}
	if err := r.validateCompletedContent(message); err != nil {
		return err
	}
	for index, content := range message.Content {
		if _, exists := r.segments[int64(index)]; !exists && (content.Type == responsesContentTypeText || content.Type == responsesContentTypeRefusal) {
			if err := r.append(int64(index), content.Type, ""); err != nil {
				return err
			}
		}
	}
	r.completion = &responsesMessageCompletion{contentCount: len(message.Content)}
	if message.JSON.Status.Valid() {
		r.completion.status = new(message.Status)
	}
	return nil
}

func (r *responsesStreamContent) bindMessage(index int64, message responses.ResponseOutputMessage) error {
	identity := responsesMessageIdentity{ItemID: message.ID, Phase: message.Phase}
	if err := identity.validate(); err != nil {
		return err
	}
	if index < 0 {
		return fmt.Errorf("%w: openai responses: message output index is negative", corechat.ErrInvalidResponse)
	}
	if previous, exists := r.messages[index]; exists {
		if previous.identity != identity {
			return fmt.Errorf("%w: openai responses: message identity or phase changed at output %d", corechat.ErrInvalidResponse, index)
		}
		return previous.requireOpen()
	}
	for otherIndex, other := range r.messages {
		if otherIndex != index && other.identity.ItemID == identity.ItemID {
			return fmt.Errorf("%w: openai responses: message identity reused", corechat.ErrInvalidResponse)
		}
	}
	if r.messages == nil {
		r.messages = make(map[int64]responsesMessageState)
	}
	r.messages[index] = responsesMessageState{identity: identity}
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
	state := r.messages[event.OutputIndex]
	if err := state.finish(message); err != nil {
		return err
	}
	r.messages[event.OutputIndex] = state
	return nil
}

func (r *responsesStreamContent) addText(event responses.ResponseTextDeltaEvent) (*corechat.PartDelta, error) {
	reported := event.JSON.OutputIndex.Valid() && event.JSON.ContentIndex.Valid() && event.JSON.Delta.Valid()
	return r.addContent(responsesContentTypeText, reported, event.OutputIndex, event.ContentIndex, event.ItemID, event.Delta)
}

func (r *responsesStreamContent) addRefusal(event responses.ResponseRefusalDeltaEvent) (*corechat.PartDelta, error) {
	reported := event.JSON.OutputIndex.Valid() && event.JSON.ContentIndex.Valid() && event.JSON.Delta.Valid()
	return r.addContent(responsesContentTypeRefusal, reported, event.OutputIndex, event.ContentIndex, event.ItemID, event.Delta)
}

// addContent appends one text or refusal increment to its message content
// entry; reported is false when the event omitted a position or the delta.
func (r *responsesStreamContent) addContent(kind string, reported bool, output, content int64, itemID, text string) (*corechat.PartDelta, error) {
	if !reported {
		return nil, fmt.Errorf("%w: openai responses: %s delta requires output index, content index, and delta", corechat.ErrInvalidResponse, kind)
	}
	if err := r.append(responsesContentPosition{output: output, content: content}, itemID, kind, text); err != nil {
		return nil, err
	}
	if text == "" {
		return nil, nil
	}
	part := corechat.NewTextDelta(text)
	if kind == responsesContentTypeRefusal {
		part = corechat.NewRefusalDelta(text)
	}
	if err := part.Metadata.Set(responsesMessageIdentityKey, r.messages[output].identity); err != nil {
		return nil, err
	}
	return &part, nil
}

// annotate attaches one citation to the open text entry the event names.
func (r *responsesStreamContent) annotate(event responses.ResponseOutputTextAnnotationAddedEvent) (*corechat.PartDelta, error) {
	citation, include, err := responsesStreamCitation(event.Annotation)
	if err != nil || !include {
		return nil, err
	}
	message := r.messages[event.OutputIndex]
	if !event.JSON.OutputIndex.Valid() || !event.JSON.ContentIndex.Valid() || !event.JSON.AnnotationIndex.Valid() ||
		event.ItemID == "" || message.identity.ItemID != event.ItemID {
		return nil, fmt.Errorf("%w: openai responses: annotation is not bound to a reported message", corechat.ErrInvalidResponse)
	}
	if err := message.requireOpen(); err != nil {
		return nil, err
	}
	if evidence, known := message.segments[event.ContentIndex]; !known || evidence.kind != responsesContentTypeText {
		return nil, fmt.Errorf("%w: openai responses: annotation references unknown text", corechat.ErrInvalidResponse)
	}
	return new(corechat.NewCitationDelta(citation)), nil
}

func (r *responsesStreamContent) append(position responsesContentPosition, itemID, kind, text string) error {
	if position.output < 0 || position.content < 0 || itemID == "" {
		return fmt.Errorf("%w: openai responses: content delta requires nonnegative indices and an item ID", corechat.ErrInvalidResponse)
	}
	if message, exists := r.messages[position.output]; exists {
		if message.identity.ItemID != itemID {
			return fmt.Errorf("%w: openai responses: message identity changed", corechat.ErrInvalidResponse)
		}
	} else if err := r.bindMessage(position.output, responses.ResponseOutputMessage{ID: itemID}); err != nil {
		return err
	}
	message := r.messages[position.output]
	if err := message.append(position.content, kind, text); err != nil {
		return err
	}
	r.messages[position.output] = message
	return nil
}

func (r *responsesStreamContent) validateTerminal(response responses.Response) error {
	for index, state := range r.messages {
		if index >= int64(len(response.Output)) || response.Output[index].Type != responsesItemTypeMessage {
			return fmt.Errorf("%w: openai responses: terminal response omitted message at index %d", corechat.ErrInvalidResponse, index)
		}
		message := response.Output[index].AsMessage()
		if err := state.validateTerminal(message); err != nil {
			return err
		}
	}
	if response.Status != responses.ResponseStatusCompleted {
		return nil
	}
	for index, item := range response.Output {
		if item.Type != responsesItemTypeMessage {
			continue
		}
		if err := r.messages[int64(index)].validateCompletedContent(item.AsMessage()); err != nil {
			return err
		}
	}
	return nil
}
