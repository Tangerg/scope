package openai

import (
	"fmt"

	"github.com/openai/openai-go/v3/responses"

	corechat "github.com/Tangerg/scope/core/chat"
	corecontent "github.com/Tangerg/scope/core/content"
)

// responsesStreamPart identifies one Core part of a Responses stream. The
// output index names the native item; kind and index name the message content
// entry or reasoning segment within it and stay zero for whole-item parts.
type responsesStreamPart struct {
	output int64
	kind   string
	index  int64
}

type responsesStreamState struct {
	parts                streamPartIndexes[responsesStreamPart]
	responseExtensionKey string
	identity             streamIdentity
	tools                responsesStreamTools
	content              responsesStreamContent
	reasoning            responsesStreamReasoning
}

func newResponsesStreamState(provider string) *responsesStreamState {
	return &responsesStreamState{
		responseExtensionKey: provider + ResponsesResponseExtensionSuffix,
		tools:                make(responsesStreamTools),
		reasoning: responsesStreamReasoning{
			codec: responsesReasoningCodec{provider: provider},
			items: make(map[int64]*responsesReasoningEvidence),
		},
	}
}

func (r *responsesStreamState) addEvent(event responses.ResponseStreamEventUnion) (*corechat.ResponseDelta, error) {
	if err := validateResponseWire[struct {
		Type responseWireString `json:"type"`
	}](event.RawJSON()); err != nil {
		return nil, fmt.Errorf("%w: openai responses: event: %w", corechat.ErrInvalidResponse, err)
	}
	if err := admitResponsesEvent(event); err != nil {
		return nil, err
	}
	var part *corechat.PartDelta
	var key responsesStreamPart
	var err error
	switch typed := event.AsAny().(type) {
	case responses.ResponseCreatedEvent:
		return nil, r.bindResponseIdentity(typed.Response)
	case responses.ResponseOutputItemAddedEvent:
		if typed.Item.Type == responsesItemTypeMessage {
			return nil, r.content.startMessage(typed)
		}
		part, err = r.tools.add(typed)
		key = responsesStreamPart{output: typed.OutputIndex}
	case responses.ResponseTextDeltaEvent:
		part, err = r.content.addText(typed)
		key = responsesStreamPart{output: typed.OutputIndex, kind: responsesContentTypeText, index: typed.ContentIndex}
	case responses.ResponseOutputTextAnnotationAddedEvent:
		part, err = r.content.annotate(typed)
		key = responsesStreamPart{output: typed.OutputIndex, kind: responsesContentTypeText, index: typed.ContentIndex}
	case responses.ResponseRefusalDeltaEvent:
		part, err = r.content.addRefusal(typed)
		key = responsesStreamPart{output: typed.OutputIndex, kind: responsesContentTypeRefusal, index: typed.ContentIndex}
	case responses.ResponseFunctionCallArgumentsDeltaEvent:
		part, err = r.tools.appendArguments(typed)
		key = responsesStreamPart{output: typed.OutputIndex}
	case responses.ResponseFunctionCallArgumentsDoneEvent:
		return nil, r.tools.finishArguments(typed)
	case responses.ResponseReasoningTextDeltaEvent:
		part, key, err = r.reasoning.delta(typed.OutputIndex, typed.JSON.OutputIndex.Valid() && typed.JSON.ContentIndex.Valid(), typed.Delta, responsesReasoningSegment{
			ItemID: typed.ItemID, Kind: responsesReasoningContent, Index: typed.ContentIndex,
		})
	case responses.ResponseReasoningSummaryTextDeltaEvent:
		part, key, err = r.reasoning.delta(typed.OutputIndex, typed.JSON.OutputIndex.Valid() && typed.JSON.SummaryIndex.Valid(), typed.Delta, responsesReasoningSegment{
			ItemID: typed.ItemID, Kind: responsesReasoningSummary, Index: typed.SummaryIndex,
		})
	case responses.ResponseReasoningSummaryPartAddedEvent:
		part, key, err = r.reasoning.delta(typed.OutputIndex, typed.JSON.OutputIndex.Valid() && typed.JSON.SummaryIndex.Valid(), typed.Part.Text, responsesReasoningSegment{
			ItemID: typed.ItemID, Kind: responsesReasoningSummary, Index: typed.SummaryIndex,
		})
	case responses.ResponseContentPartAddedEvent:
		if stateErr := r.content.messages[typed.OutputIndex].requireOpen(); stateErr != nil {
			return nil, stateErr
		}
		if typed.Part.Type != responsesReasoningContentText {
			return nil, nil
		}
		part, key, err = r.reasoning.delta(typed.OutputIndex, typed.JSON.OutputIndex.Valid() && typed.JSON.ContentIndex.Valid(), typed.Part.Text, responsesReasoningSegment{
			ItemID: typed.ItemID, Kind: responsesReasoningContent, Index: typed.ContentIndex,
		})
	case responses.ResponseContentPartDoneEvent, responses.ResponseTextDoneEvent, responses.ResponseRefusalDoneEvent:
		return nil, r.content.messages[event.OutputIndex].requireOpen()
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
	return r.deltaResponse(key, *part)
}

// admitResponsesEvent checks the wire shape of every event variant the stream
// consumes. SDK union fields erase variant-specific types, so only a consumed
// variant selects an admission shape; unrelated native events stay opaque.
func admitResponsesEvent(event responses.ResponseStreamEventUnion) error {
	switch event.AsAny().(type) {
	case responses.ResponseCreatedEvent, responses.ResponseCompletedEvent, responses.ResponseIncompleteEvent, responses.ResponseFailedEvent:
		return validateResponsesEvent[responsesSnapshotEventWire](event)
	case responses.ResponseOutputItemAddedEvent, responses.ResponseOutputItemDoneEvent:
		return validateResponsesEvent[responsesItemEventWire](event)
	case responses.ResponseTextDeltaEvent, responses.ResponseRefusalDeltaEvent, responses.ResponseFunctionCallArgumentsDeltaEvent, responses.ResponseReasoningTextDeltaEvent, responses.ResponseReasoningSummaryTextDeltaEvent:
		return validateResponsesEvent[responsesDeltaEventWire](event)
	case responses.ResponseOutputTextAnnotationAddedEvent:
		return validateResponsesEvent[responsesAnnotationEventWire](event)
	case responses.ResponseFunctionCallArgumentsDoneEvent:
		return validateResponsesEvent[responsesArgumentsDoneEventWire](event)
	case responses.ResponseReasoningSummaryPartAddedEvent, responses.ResponseContentPartAddedEvent, responses.ResponseContentPartDoneEvent, responses.ResponseTextDoneEvent, responses.ResponseRefusalDoneEvent:
		return validateResponsesEvent[responsesContentEventWire](event)
	case responses.ResponseErrorEvent:
		return validateResponsesEvent[responsesErrorEventWire](event)
	default:
		return nil
	}
}

func (r *responsesStreamState) finishItem(event responses.ResponseOutputItemDoneEvent) (*corechat.ResponseDelta, error) {
	if event.Item.Type == responsesItemTypeMessage {
		return nil, r.content.finishMessage(event)
	}
	if event.Item.Type == responsesItemTypeFunctionCall {
		return nil, r.tools.finishItem(event)
	}
	if event.Item.Type != responsesItemTypeReasoning {
		return nil, nil
	}
	part, key, err := r.reasoning.finish(event)
	if err != nil {
		return nil, err
	}
	return r.deltaResponse(key, *part)
}

func (r *responsesStreamState) bindResponseIdentity(response responses.Response) error {
	return r.identity.bind(response.ID, string(response.Model), int64(response.CreatedAt))
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
		if err := r.reasoning.validateTerminal(response); err != nil {
			return nil, err
		}
	}
	return responsesTerminalDelta(&response, r.responseExtensionKey, r.identity)
}

func (r *responsesStreamState) deltaResponse(key responsesStreamPart, part corechat.PartDelta) (*corechat.ResponseDelta, error) {
	part.Index = r.parts.of(key)
	response := &corechat.ResponseDelta{
		Parts:    []corechat.PartDelta{part},
		Metadata: r.identity.metadata(),
	}
	if err := response.Validate(); err != nil {
		return nil, err
	}
	return response, nil
}

func responsesStreamCitation(annotation responses.ResponseOutputTextAnnotationAddedEventAnnotationUnion) (corecontent.Citation, bool, error) {
	switch typed := annotation.AsAny().(type) {
	case responses.ResponseOutputTextAnnotationAddedEventAnnotationURLCitation:
		return corecontent.Citation{
			Source: corecontent.CitationSource{Kind: corecontent.CitationSourceURI, Value: typed.URL},
			Title:  typed.Title,
		}, true, nil
	case responses.ResponseOutputTextAnnotationAddedEventAnnotationFileCitation:
		return corecontent.Citation{
			Source: corecontent.CitationSource{Kind: corecontent.CitationSourceReference, Value: typed.FileID},
			Title:  typed.Filename,
		}, true, nil
	case responses.ResponseOutputTextAnnotationAddedEventAnnotationContainerFileCitation:
		return corecontent.Citation{
			Source: corecontent.CitationSource{Kind: corecontent.CitationSourceReference, Value: typed.FileID},
			Title:  typed.Filename,
		}, true, nil
	case responses.ResponseOutputTextAnnotationAddedEventAnnotationFilePath:
		return corecontent.Citation{
			Source: corecontent.CitationSource{Kind: corecontent.CitationSourceReference, Value: typed.FileID},
		}, true, nil
	case nil:
		return corecontent.Citation{}, false, nil
	default:
		return corecontent.Citation{}, false, fmt.Errorf("openai responses: unsupported stream annotation %T", typed)
	}
}
