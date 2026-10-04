package openai

import (
	"errors"
	"fmt"
	"time"

	"github.com/openai/openai-go/v3/responses"

	corechat "github.com/Tangerg/scope/core/chat"
)

type responsesToolIdentity struct {
	id   string
	name string
}

type responsesStreamState struct {
	responseID string
	model      string
	createdAt  time.Time
	tools      map[string]responsesToolIdentity
	reasoning  map[string]responsesReasoningSegment
}

func newResponsesStreamState() *responsesStreamState {
	return &responsesStreamState{
		tools: make(map[string]responsesToolIdentity), reasoning: make(map[string]responsesReasoningSegment),
	}
}

func (r *responsesStreamState) addEvent(event responses.ResponseStreamEventUnion) (*corechat.ResponseDelta, bool, error) {
	switch typed := event.AsAny().(type) {
	case responses.ResponseCreatedEvent:
		r.responseID = typed.Response.ID
		r.model = string(typed.Response.Model)
		if typed.Response.CreatedAt > 0 {
			r.createdAt = time.Unix(int64(typed.Response.CreatedAt), 0).UTC()
		}
		return nil, false, nil
	case responses.ResponseOutputItemAddedEvent:
		if typed.Item.Type != responsesItemTypeFunctionCall {
			return nil, false, nil
		}
		call := typed.Item.AsFunctionCall()
		id := call.CallID
		if id == "" {
			id = call.ID
		}
		if id == "" || call.Name == "" {
			return nil, false, errors.New("openai responses: stream function call lacks ID or name")
		}
		r.tools[call.ID] = responsesToolIdentity{id: id, name: call.Name}
		return r.deltaResponse(corechat.NewToolCallDelta(corechat.ToolCallDelta{ID: id, Name: call.Name}))
	case responses.ResponseTextDeltaEvent:
		if typed.Delta == "" {
			return nil, false, nil
		}
		return r.deltaResponse(corechat.NewTextDelta(typed.Delta))
	case responses.ResponseOutputTextAnnotationAddedEvent:
		citation, include, err := responsesStreamCitation(typed.Annotation)
		if err != nil || !include {
			return nil, false, err
		}
		return r.deltaResponse(corechat.NewCitationDelta(citation))
	case responses.ResponseRefusalDeltaEvent:
		if typed.Delta == "" {
			return nil, false, nil
		}
		return r.deltaResponse(corechat.NewRefusalDelta(typed.Delta))
	case responses.ResponseFunctionCallArgumentsDeltaEvent:
		if typed.Delta == "" {
			return nil, false, nil
		}
		identity, ok := r.tools[typed.ItemID]
		if !ok {
			return nil, false, fmt.Errorf("openai responses: arguments delta for unknown item %q", typed.ItemID)
		}
		return r.deltaResponse(corechat.NewToolCallDelta(corechat.ToolCallDelta{ID: identity.id, Name: identity.name, Arguments: typed.Delta}))
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
			return nil, false, nil
		}
		return r.reasoningDelta(typed.Part.Text, responsesReasoningSegment{
			ItemID: typed.ItemID, Kind: responsesReasoningContent, Index: typed.ContentIndex,
		})
	case responses.ResponseOutputItemDoneEvent:
		if typed.Item.Type != responsesItemTypeReasoning {
			return nil, false, nil
		}
		reasoning := typed.Item.AsReasoning()
		state, err := completedResponsesReasoningState(reasoning)
		if err != nil {
			return nil, false, fmt.Errorf("openai responses: stream reasoning item: %w", err)
		}
		frame, err := encodeResponsesReasoningFrame(state)
		if err != nil {
			return nil, false, fmt.Errorf("openai responses: stream reasoning item: %w", err)
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
			return nil, false, err
		}
		return r.deltaResponse(part)
	case responses.ResponseCompletedEvent:
		delta, err := responsesTerminalDelta(&typed.Response)
		return delta, err == nil, err
	case responses.ResponseIncompleteEvent:
		delta, err := responsesTerminalDelta(&typed.Response)
		return delta, err == nil, err
	case responses.ResponseFailedEvent:
		delta, err := responsesTerminalDelta(&typed.Response)
		return delta, err == nil, err
	case responses.ResponseErrorEvent:
		return nil, false, fmt.Errorf("openai responses: %s: %s", typed.Code, typed.Message)
	default:
		return nil, false, nil
	}
}

func (r *responsesStreamState) reasoningDelta(text string, segment responsesReasoningSegment) (*corechat.ResponseDelta, bool, error) {
	frame, err := encodeResponsesReasoningFrame(responsesReasoningState{Segment: &segment})
	if err != nil {
		return nil, false, err
	}
	part := corechat.NewReasoningDelta(text, frame)
	if err := part.Metadata.Set(responsesReasoningIdentityKey, segment); err != nil {
		return nil, false, err
	}
	r.reasoning[segment.ItemID] = segment
	return r.deltaResponse(part)
}

func (r *responsesStreamState) deltaResponse(part corechat.PartDelta) (*corechat.ResponseDelta, bool, error) {
	response := &corechat.ResponseDelta{
		Parts:    []corechat.PartDelta{part},
		Metadata: &corechat.ResponseMetadata{ID: r.responseID, Model: r.model, CreatedAt: r.createdAt},
	}
	if err := response.Validate(); err != nil {
		return nil, false, err
	}
	return response, true, nil
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
