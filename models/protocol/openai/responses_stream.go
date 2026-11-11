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
	var part *corechat.PartDelta
	var key responsesStreamPart
	var err error
	// SDK union fields erase variant-specific types. Only a consumed variant
	// selects an admission shape; unrelated native events remain opaque.
	switch typed := event.AsAny().(type) {
	case responses.ResponseCreatedEvent:
		if wireErr := validateResponsesEvent[responsesSnapshotEventWire](event); wireErr != nil {
			return nil, wireErr
		}
		return nil, r.bindResponseIdentity(typed.Response)
	case responses.ResponseOutputItemAddedEvent:
		if wireErr := validateResponsesEvent[responsesItemEventWire](event); wireErr != nil {
			return nil, wireErr
		}
		if typed.Item.Type == responsesItemTypeMessage {
			return nil, r.content.startMessage(typed)
		}
		part, err = r.tools.add(typed)
		key = responsesStreamPart{output: typed.OutputIndex}
	case responses.ResponseTextDeltaEvent:
		if wireErr := validateResponsesEvent[responsesDeltaEventWire](event); wireErr != nil {
			return nil, wireErr
		}
		part, err = r.content.addText(typed)
		key = responsesStreamPart{output: typed.OutputIndex, kind: responsesContentTypeText, index: typed.ContentIndex}
	case responses.ResponseOutputTextAnnotationAddedEvent:
		if wireErr := validateResponsesEvent[responsesAnnotationEventWire](event); wireErr != nil {
			return nil, wireErr
		}
		citation, include, citationErr := responsesStreamCitation(typed.Annotation)
		if citationErr != nil || !include {
			return nil, citationErr
		}
		if !typed.JSON.OutputIndex.Valid() || !typed.JSON.ContentIndex.Valid() || !typed.JSON.AnnotationIndex.Valid() ||
			typed.ItemID == "" || r.content.messages[typed.OutputIndex].identity.ItemID != typed.ItemID {
			return nil, fmt.Errorf("%w: openai responses: annotation is not bound to a reported message", corechat.ErrInvalidResponse)
		}
		if stateErr := r.content.messages[typed.OutputIndex].requireOpen(); stateErr != nil {
			return nil, stateErr
		}
		if evidence, known := r.content.messages[typed.OutputIndex].segments[typed.ContentIndex]; !known || evidence.kind != responsesContentTypeText {
			return nil, fmt.Errorf("%w: openai responses: annotation references unknown text", corechat.ErrInvalidResponse)
		}
		part = new(corechat.NewCitationDelta(citation))
		key = responsesStreamPart{output: typed.OutputIndex, kind: responsesContentTypeText, index: typed.ContentIndex}
	case responses.ResponseRefusalDeltaEvent:
		if wireErr := validateResponsesEvent[responsesDeltaEventWire](event); wireErr != nil {
			return nil, wireErr
		}
		part, err = r.content.addRefusal(typed)
		key = responsesStreamPart{output: typed.OutputIndex, kind: responsesContentTypeRefusal, index: typed.ContentIndex}
	case responses.ResponseFunctionCallArgumentsDeltaEvent:
		if wireErr := validateResponsesEvent[responsesDeltaEventWire](event); wireErr != nil {
			return nil, wireErr
		}
		part, err = r.tools.appendArguments(typed)
		key = responsesStreamPart{output: typed.OutputIndex}
	case responses.ResponseFunctionCallArgumentsDoneEvent:
		if wireErr := validateResponsesEvent[responsesArgumentsDoneEventWire](event); wireErr != nil {
			return nil, wireErr
		}
		return nil, r.tools.finishArguments(typed)
	case responses.ResponseReasoningTextDeltaEvent:
		if wireErr := validateResponsesEvent[responsesDeltaEventWire](event); wireErr != nil {
			return nil, wireErr
		}
		part, key, err = r.reasoning.delta(typed.OutputIndex, typed.JSON.OutputIndex.Valid() && typed.JSON.ContentIndex.Valid(), typed.Delta, responsesReasoningSegment{
			ItemID: typed.ItemID, Kind: responsesReasoningContent, Index: typed.ContentIndex,
		})
	case responses.ResponseReasoningSummaryTextDeltaEvent:
		if wireErr := validateResponsesEvent[responsesDeltaEventWire](event); wireErr != nil {
			return nil, wireErr
		}
		part, key, err = r.reasoning.delta(typed.OutputIndex, typed.JSON.OutputIndex.Valid() && typed.JSON.SummaryIndex.Valid(), typed.Delta, responsesReasoningSegment{
			ItemID: typed.ItemID, Kind: responsesReasoningSummary, Index: typed.SummaryIndex,
		})
	case responses.ResponseReasoningSummaryPartAddedEvent:
		if wireErr := validateResponsesEvent[responsesContentEventWire](event); wireErr != nil {
			return nil, wireErr
		}
		part, key, err = r.reasoning.delta(typed.OutputIndex, typed.JSON.OutputIndex.Valid() && typed.JSON.SummaryIndex.Valid(), typed.Part.Text, responsesReasoningSegment{
			ItemID: typed.ItemID, Kind: responsesReasoningSummary, Index: typed.SummaryIndex,
		})
	case responses.ResponseContentPartAddedEvent:
		if wireErr := validateResponsesEvent[responsesContentEventWire](event); wireErr != nil {
			return nil, wireErr
		}
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
		if wireErr := validateResponsesEvent[responsesContentEventWire](event); wireErr != nil {
			return nil, wireErr
		}
		return nil, r.content.messages[event.OutputIndex].requireOpen()
	case responses.ResponseOutputItemDoneEvent:
		if wireErr := validateResponsesEvent[responsesItemEventWire](event); wireErr != nil {
			return nil, wireErr
		}
		return r.finishItem(typed)
	case responses.ResponseCompletedEvent:
		if wireErr := validateResponsesEvent[responsesSnapshotEventWire](event); wireErr != nil {
			return nil, wireErr
		}
		return r.terminalDelta(typed.Response, responses.ResponseStatusCompleted)
	case responses.ResponseIncompleteEvent:
		if wireErr := validateResponsesEvent[responsesSnapshotEventWire](event); wireErr != nil {
			return nil, wireErr
		}
		return r.terminalDelta(typed.Response, responses.ResponseStatusIncomplete)
	case responses.ResponseFailedEvent:
		if wireErr := validateResponsesEvent[responsesSnapshotEventWire](event); wireErr != nil {
			return nil, wireErr
		}
		return r.terminalDelta(typed.Response, responses.ResponseStatusFailed)
	case responses.ResponseErrorEvent:
		if wireErr := validateResponsesEvent[responsesErrorEventWire](event); wireErr != nil {
			return nil, wireErr
		}
		return nil, fmt.Errorf("openai responses: %s: %s", typed.Code, typed.Message)
	}
	if err != nil || part == nil {
		return nil, err
	}
	return r.deltaResponse(key, *part)
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
