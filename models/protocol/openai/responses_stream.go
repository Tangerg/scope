package openai

import (
	"fmt"
	"time"

	"github.com/openai/openai-go/v3/responses"

	corechat "github.com/Tangerg/scope/core/chat"
)

type responsesStreamState struct {
	responseExtensionKey string
	responseID           string
	model                string
	createdAt            time.Time
	tools                responsesStreamTools
	content              responsesStreamContent
	reasoning            map[string]responsesReasoningSegment
}

func newResponsesStreamState(extensionKey string) *responsesStreamState {
	return &responsesStreamState{
		responseExtensionKey: extensionKey,
		tools:                make(responsesStreamTools),
		content:              make(responsesStreamContent),
		reasoning:            make(map[string]responsesReasoningSegment),
	}
}

func (r *responsesStreamState) addEvent(event responses.ResponseStreamEventUnion) (*corechat.ResponseDelta, error) {
	var part *corechat.PartDelta
	var err error
	switch typed := event.AsAny().(type) {
	case responses.ResponseCreatedEvent:
		return nil, r.bindResponseIdentity(typed.Response)
	case responses.ResponseOutputItemAddedEvent:
		part, err = r.tools.add(typed)
	case responses.ResponseTextDeltaEvent:
		part, err = r.content.addText(typed)
	case responses.ResponseOutputTextAnnotationAddedEvent:
		citation, include, citationErr := responsesStreamCitation(typed.Annotation)
		if citationErr != nil || !include {
			return nil, citationErr
		}
		return r.deltaResponse(corechat.NewCitationDelta(citation))
	case responses.ResponseRefusalDeltaEvent:
		part, err = r.content.addRefusal(typed)
	case responses.ResponseFunctionCallArgumentsDeltaEvent:
		part, err = r.tools.appendArguments(typed)
	case responses.ResponseFunctionCallArgumentsDoneEvent:
		return nil, r.tools.finishArguments(typed)
	case responses.ResponseReasoningTextDeltaEvent:
		return r.reasoningDelta(typed.Delta, responsesReasoningSegment{
			ItemID: typed.ItemID, Kind: responsesReasoningContent, Index: typed.ContentIndex,
		})
	case responses.ResponseReasoningSummaryTextDeltaEvent:
		return r.reasoningDelta(typed.Delta, responsesReasoningSegment{
			ItemID: typed.ItemID, Kind: responsesReasoningSummary, Index: typed.SummaryIndex,
		})
	case responses.ResponseReasoningSummaryPartAddedEvent:
		return r.reasoningDelta(typed.Part.Text, responsesReasoningSegment{
			ItemID: typed.ItemID, Kind: responsesReasoningSummary, Index: typed.SummaryIndex,
		})
	case responses.ResponseContentPartAddedEvent:
		if typed.Part.Type != responsesReasoningContentText {
			return nil, nil
		}
		return r.reasoningDelta(typed.Part.Text, responsesReasoningSegment{
			ItemID: typed.ItemID, Kind: responsesReasoningContent, Index: typed.ContentIndex,
		})
	case responses.ResponseOutputItemDoneEvent:
		return r.finishItem(typed)
	case responses.ResponseCompletedEvent:
		return r.terminalDelta(typed.Response, responses.ResponseStatusCompleted)
	case responses.ResponseIncompleteEvent:
		return r.terminalDelta(typed.Response, responses.ResponseStatusIncomplete)
	case responses.ResponseFailedEvent:
		return r.terminalDelta(typed.Response, responses.ResponseStatusFailed)
	case responses.ResponseErrorEvent:
		return nil, fmt.Errorf("openai responses: %s: %s", typed.Code, typed.Message)
	}
	if err != nil || part == nil {
		return nil, err
	}
	return r.deltaResponse(*part)
}

func (r *responsesStreamState) finishItem(event responses.ResponseOutputItemDoneEvent) (*corechat.ResponseDelta, error) {
	if event.Item.Type == responsesItemTypeFunctionCall {
		return nil, r.tools.finishItem(event)
	}
	if event.Item.Type != responsesItemTypeReasoning {
		return nil, nil
	}
	reasoning := event.Item.AsReasoning()
	state, err := completedResponsesReasoningState(reasoning)
	if err != nil {
		return nil, fmt.Errorf("openai responses: stream reasoning item: %w", err)
	}
	frame, err := encodeResponsesReasoningFrame(state)
	if err != nil {
		return nil, fmt.Errorf("openai responses: stream reasoning item: %w", err)
	}
	part := corechat.NewReasoningDelta("", frame)
	if segment, exists := r.reasoning[reasoning.ID]; exists {
		err = part.Metadata.Set(responsesReasoningIdentityKey, segment)
	} else {
		err = part.Metadata.Set(responsesReasoningIdentityKey, struct {
			ItemID string `json:"item_id"`
		}{ItemID: reasoning.ID})
	}
	if err != nil {
		return nil, err
	}
	return r.deltaResponse(part)
}

func (r *responsesStreamState) bindResponseIdentity(response responses.Response) error {
	if response.ID != "" {
		if r.responseID != "" && r.responseID != response.ID {
			return fmt.Errorf("%w: openai responses: response ID changed", corechat.ErrInvalidResponse)
		}
		r.responseID = response.ID
	}
	if response.Model != "" {
		if r.model != "" && r.model != string(response.Model) {
			return fmt.Errorf("%w: openai responses: response model changed", corechat.ErrInvalidResponse)
		}
		r.model = string(response.Model)
	}
	if response.CreatedAt > 0 {
		createdAt := time.Unix(int64(response.CreatedAt), 0).UTC()
		if !r.createdAt.IsZero() && r.createdAt != createdAt {
			return fmt.Errorf("%w: openai responses: response creation time changed", corechat.ErrInvalidResponse)
		}
		r.createdAt = createdAt
	}
	return nil
}

func (r *responsesStreamState) terminalDelta(response responses.Response, status responses.ResponseStatus) (*corechat.ResponseDelta, error) {
	if response.Status != status {
		return nil, fmt.Errorf("%w: openai responses: response.%s carries status %q", corechat.ErrInvalidResponse, status, response.Status)
	}
	if err := r.bindResponseIdentity(response); err != nil {
		return nil, err
	}
	if status != responses.ResponseStatusFailed {
		if err := r.tools.validateTerminal(response); err != nil {
			return nil, err
		}
		if err := r.content.validateTerminal(response); err != nil {
			return nil, err
		}
	}
	return responsesTerminalDelta(&response, r.responseExtensionKey)
}

func (r *responsesStreamState) reasoningDelta(text string, segment responsesReasoningSegment) (*corechat.ResponseDelta, error) {
	frame, err := encodeResponsesReasoningFrame(responsesReasoningState{Segment: &segment})
	if err != nil {
		return nil, err
	}
	part := corechat.NewReasoningDelta(text, frame)
	if err := part.Metadata.Set(responsesReasoningIdentityKey, segment); err != nil {
		return nil, err
	}
	r.reasoning[segment.ItemID] = segment
	return r.deltaResponse(part)
}

func (r *responsesStreamState) deltaResponse(part corechat.PartDelta) (*corechat.ResponseDelta, error) {
	response := &corechat.ResponseDelta{
		Parts:    []corechat.PartDelta{part},
		Metadata: &corechat.ResponseMetadata{ID: r.responseID, Model: r.model, CreatedAt: r.createdAt},
	}
	if err := response.Validate(); err != nil {
		return nil, err
	}
	return response, nil
}

func responsesStreamCitation(annotation responses.ResponseOutputTextAnnotationAddedEventAnnotationUnion) (corechat.Citation, bool, error) {
	switch typed := annotation.AsAny().(type) {
	case responses.ResponseOutputTextAnnotationAddedEventAnnotationURLCitation:
		return corechat.Citation{
			Source: corechat.CitationSource{Kind: corechat.CitationSourceURI, Value: typed.URL},
			Title:  typed.Title,
		}, true, nil
	case responses.ResponseOutputTextAnnotationAddedEventAnnotationFileCitation:
		return corechat.Citation{
			Source: corechat.CitationSource{Kind: corechat.CitationSourceReference, Value: typed.FileID},
			Title:  typed.Filename,
		}, true, nil
	case responses.ResponseOutputTextAnnotationAddedEventAnnotationContainerFileCitation:
		return corechat.Citation{
			Source: corechat.CitationSource{Kind: corechat.CitationSourceReference, Value: typed.FileID},
			Title:  typed.Filename,
		}, true, nil
	case responses.ResponseOutputTextAnnotationAddedEventAnnotationFilePath:
		return corechat.Citation{
			Source: corechat.CitationSource{Kind: corechat.CitationSourceReference, Value: typed.FileID},
		}, true, nil
	case nil:
		return corechat.Citation{}, false, nil
	default:
		return corechat.Citation{}, false, fmt.Errorf("openai responses: unsupported stream annotation %T", typed)
	}
}
