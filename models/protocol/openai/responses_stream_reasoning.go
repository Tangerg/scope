package openai

import (
	"fmt"

	"github.com/openai/openai-go/v3/responses"

	corechat "github.com/Tangerg/scope/core/chat"
)

// Evidence verifies provider projections without replacing Core's ownership of
// visible reasoning text or retaining a second replay representation.
type responsesReasoningEvidence struct {
	itemID      string
	segments    map[responsesReasoningSegment][]byte
	lastSegment *responsesReasoningSegment
	completion  *responsesReasoningCompletion
}

type responsesReasoningCompletion struct {
	encryptedContent        string
	encryptedContentPresent bool
	status                  responses.ResponseReasoningItemStatus
}

func (r *responsesReasoningEvidence) append(text string, segment responsesReasoningSegment) error {
	if r.completion != nil {
		return fmt.Errorf("%w: openai responses: reasoning changed after item completion", corechat.ErrInvalidResponse)
	}
	if err := segment.validate(); err != nil {
		return fmt.Errorf("%w: openai responses: %w", corechat.ErrInvalidResponse, err)
	}
	if r.segments == nil {
		r.segments = make(map[responsesReasoningSegment][]byte)
	}
	r.segments[segment] = append(r.segments[segment], text...)
	r.lastSegment = &segment
	return nil
}

func (r *responsesReasoningEvidence) validateText(item responses.ResponseReasoningItem) error {
	if len(r.segments) != len(item.Summary)+len(item.Content) {
		return fmt.Errorf("%w: openai responses: reasoning segment structure changed", corechat.ErrInvalidResponse)
	}
	for index, summary := range item.Summary {
		segment := responsesReasoningSegment{ItemID: r.itemID, Kind: responsesReasoningSummary, Index: int64(index)}
		text, exists := r.segments[segment]
		if !exists || string(text) != summary.Text {
			return fmt.Errorf("%w: openai responses: reasoning summary %d changed", corechat.ErrInvalidResponse, index)
		}
	}
	for index, content := range item.Content {
		segment := responsesReasoningSegment{ItemID: r.itemID, Kind: responsesReasoningContent, Index: int64(index)}
		text, exists := r.segments[segment]
		if !exists || string(text) != content.Text {
			return fmt.Errorf("%w: openai responses: reasoning content %d changed", corechat.ErrInvalidResponse, index)
		}
	}
	return nil
}

type responsesStreamReasoning struct {
	codec responsesReasoningCodec
	items map[int64]*responsesReasoningEvidence
}

func (r responsesStreamReasoning) bind(index int64, itemID string) (*responsesReasoningEvidence, error) {
	if index < 0 || itemID == "" {
		return nil, fmt.Errorf("%w: openai responses: reasoning requires a nonnegative output index and item ID", corechat.ErrInvalidResponse)
	}
	if evidence, exists := r.items[index]; exists {
		if evidence.itemID != itemID {
			return nil, fmt.Errorf("%w: openai responses: reasoning item identity changed at output %d", corechat.ErrInvalidResponse, index)
		}
		return evidence, nil
	}
	for _, other := range r.items {
		if other.itemID == itemID {
			return nil, fmt.Errorf("%w: openai responses: reasoning item identity reused", corechat.ErrInvalidResponse)
		}
	}
	evidence := &responsesReasoningEvidence{itemID: itemID}
	r.items[index] = evidence
	return evidence, nil
}

func (r responsesStreamReasoning) delta(index int64, indexPresent bool, text string, segment responsesReasoningSegment) (*corechat.PartDelta, responsesStreamPart, error) {
	if !indexPresent {
		return nil, responsesStreamPart{}, fmt.Errorf("%w: openai responses: reasoning delta requires its output and segment indices", corechat.ErrInvalidResponse)
	}
	evidence, err := r.bind(index, segment.ItemID)
	if err != nil {
		return nil, responsesStreamPart{}, err
	}
	if appendErr := evidence.append(text, segment); appendErr != nil {
		return nil, responsesStreamPart{}, appendErr
	}
	frame, err := r.codec.encodeFrame(responsesReasoningState{Segment: &segment})
	if err != nil {
		return nil, responsesStreamPart{}, err
	}
	return new(corechat.NewReasoningDelta(text, frame)), segment.part(index), nil
}

// finish publishes the item's remaining native state on its last segment, or
// on a part of its own when the item streamed no text.
func (r responsesStreamReasoning) finish(event responses.ResponseOutputItemDoneEvent) (*corechat.PartDelta, responsesStreamPart, error) {
	if !event.JSON.OutputIndex.Valid() {
		return nil, responsesStreamPart{}, fmt.Errorf("%w: openai responses: reasoning completion requires an output index", corechat.ErrInvalidResponse)
	}
	item := event.Item.AsReasoning()
	evidence, err := r.bind(event.OutputIndex, item.ID)
	if err != nil {
		return nil, responsesStreamPart{}, err
	}
	if evidence.completion != nil {
		return nil, responsesStreamPart{}, fmt.Errorf("%w: openai responses: reasoning item completed twice", corechat.ErrInvalidResponse)
	}
	state, err := completedResponsesReasoningState(item)
	if err != nil {
		return nil, responsesStreamPart{}, fmt.Errorf("%w: openai responses: reasoning completion: %w", corechat.ErrInvalidResponse, err)
	}
	if validationErr := evidence.validateText(item); validationErr != nil {
		return nil, responsesStreamPart{}, validationErr
	}
	frame, err := r.codec.encodeFrame(state)
	if err != nil {
		return nil, responsesStreamPart{}, fmt.Errorf("%w: openai responses: reasoning completion: %w", corechat.ErrInvalidResponse, err)
	}
	key := responsesStreamPart{output: event.OutputIndex}
	if evidence.lastSegment != nil {
		key = evidence.lastSegment.part(event.OutputIndex)
	}
	evidence.completion = &responsesReasoningCompletion{
		encryptedContent: item.EncryptedContent, encryptedContentPresent: item.JSON.EncryptedContent.Valid(), status: item.Status,
	}
	return new(corechat.NewReasoningDelta("", frame)), key, nil
}

func (r responsesStreamReasoning) validateTerminal(response responses.Response) error {
	for index, evidence := range r.items {
		if index >= int64(len(response.Output)) || response.Output[index].Type != responsesItemTypeReasoning || response.Output[index].ID != evidence.itemID {
			return fmt.Errorf("%w: openai responses: terminal response omitted or changed reasoning at output %d", corechat.ErrInvalidResponse, index)
		}
		item := response.Output[index].AsReasoning()
		if _, err := completedResponsesReasoningState(item); err != nil {
			return fmt.Errorf("%w: openai responses: terminal reasoning: %w", corechat.ErrInvalidResponse, err)
		}
		if err := evidence.validateText(item); err != nil {
			return err
		}
		if evidence.completion == nil {
			if response.Status == responses.ResponseStatusCompleted {
				return fmt.Errorf("%w: openai responses: completed response has unfinished reasoning at output %d", corechat.ErrInvalidResponse, index)
			}
			continue
		}
		completed := evidence.completion
		if item.EncryptedContent != completed.encryptedContent || item.JSON.EncryptedContent.Valid() != completed.encryptedContentPresent || item.Status != completed.status {
			return fmt.Errorf("%w: openai responses: terminal response changed completed reasoning state", corechat.ErrInvalidResponse)
		}
	}
	if response.Status != responses.ResponseStatusCompleted {
		return nil
	}
	for index, item := range response.Output {
		if item.Type == responsesItemTypeReasoning && r.items[int64(index)] == nil {
			return fmt.Errorf("%w: openai responses: completed response introduced reasoning at output %d", corechat.ErrInvalidResponse, index)
		}
	}
	return nil
}
