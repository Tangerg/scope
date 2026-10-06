package openai

import (
	"fmt"
	"time"

	"github.com/openai/openai-go/v3/responses"

	corechat "github.com/Tangerg/scope/core/chat"
)

type responsesStreamTool struct {
	itemID string
	callID string
	name   string
	open   bool
}

type responsesStreamState struct {
	responseID string
	model      string
	createdAt  time.Time
	tools      map[int64]responsesStreamTool
	reasoning  map[string]responsesReasoningSegment
}

func newResponsesStreamState() *responsesStreamState {
	return &responsesStreamState{
		tools: make(map[int64]responsesStreamTool), reasoning: make(map[string]responsesReasoningSegment),
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
		if typed.OutputIndex < 0 || call.CallID == "" || call.Name == "" {
			return nil, false, fmt.Errorf("openai responses: %w: function call lacks a valid index, call ID, or name", corechat.ErrInvalidResponse)
		}
		if _, exists := r.tools[typed.OutputIndex]; exists {
			return nil, false, fmt.Errorf("openai responses: %w: tool item %d added twice", corechat.ErrInvalidResponse, typed.OutputIndex)
		}
		for _, other := range r.tools {
			if other.callID == call.CallID || call.ID != "" && other.itemID == call.ID {
				return nil, false, fmt.Errorf("openai responses: %w: tool identity reused at output index %d", corechat.ErrInvalidResponse, typed.OutputIndex)
			}
		}
		response, include, err := r.deltaResponse(corechat.NewToolCallDelta(corechat.ToolCallDelta{ID: call.CallID, Name: call.Name, Arguments: call.Arguments}))
		if err == nil {
			r.tools[typed.OutputIndex] = responsesStreamTool{itemID: call.ID, callID: call.CallID, name: call.Name, open: true}
		}
		return response, include, err
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
		tool, exists := r.tools[typed.OutputIndex]
		if !exists || !tool.open || typed.ItemID == "" || tool.itemID != typed.ItemID {
			return nil, false, fmt.Errorf("openai responses: %w: arguments delta for closed, unknown, or mismatched tool item %q at index %d", corechat.ErrInvalidResponse, typed.ItemID, typed.OutputIndex)
		}
		if typed.Delta == "" {
			return nil, false, nil
		}
		return r.deltaResponse(corechat.NewToolCallDelta(corechat.ToolCallDelta{ID: tool.callID, Name: tool.name, Arguments: typed.Delta}))
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
		if typed.Item.Type == responsesItemTypeFunctionCall {
			call := typed.Item.AsFunctionCall()
			tool, exists := r.tools[typed.OutputIndex]
			if !exists || !tool.open || !tool.matches(call) {
				return nil, false, fmt.Errorf("openai responses: %w: completion of closed, unknown, or changed tool item at index %d", corechat.ErrInvalidResponse, typed.OutputIndex)
			}
			tool.open = false
			r.tools[typed.OutputIndex] = tool
			return nil, false, nil
		}
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
		if err := r.validateTerminalTools(typed.Response); err != nil {
			return nil, false, err
		}
		delta, err := responsesTerminalDelta(&typed.Response)
		return delta, err == nil, err
	case responses.ResponseIncompleteEvent:
		if err := r.validateTerminalTools(typed.Response); err != nil {
			return nil, false, err
		}
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

func (r responsesStreamTool) matches(call responses.ResponseFunctionToolCall) bool {
	return r.callID == call.CallID && r.name == call.Name && (r.itemID == "" || r.itemID == call.ID)
}

func (r *responsesStreamState) validateTerminalTools(response responses.Response) error {
	for index, tool := range r.tools {
		if response.Status == responses.ResponseStatusCompleted && tool.open {
			return fmt.Errorf("openai responses: %w: tool item %d is still open at response.completed", corechat.ErrInvalidResponse, index)
		}
		if index >= int64(len(response.Output)) || response.Output[index].Type != responsesItemTypeFunctionCall || !tool.matches(response.Output[index].AsFunctionCall()) {
			return fmt.Errorf("openai responses: %w: terminal response changed tool identity at index %d", corechat.ErrInvalidResponse, index)
		}
	}
	return nil
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
