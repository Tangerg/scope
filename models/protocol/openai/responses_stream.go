package openai

import (
	"errors"
	"fmt"

	"github.com/openai/openai-go/v3/packages/respjson"
	"github.com/openai/openai-go/v3/responses"

	corechat "github.com/Tangerg/scope/core/chat"
	corecontent "github.com/Tangerg/scope/core/content"
)

type responsesStreamState struct {
	lastText             *responsesContentPosition
	responseExtensionKey string
	identity             streamIdentity
	tools                responsesStreamTools
	content              responsesStreamContent
	reasoning            responsesStreamReasoning
}

func newResponsesStreamState(provider string) *responsesStreamState {
	return &responsesStreamState{
		responseExtensionKey: provider + "/responses_response",
		tools:                make(responsesStreamTools),
		reasoning: responsesStreamReasoning{
			codec: responsesReasoningCodec{provider: provider},
			items: make(map[int64]*responsesReasoningEvidence),
		},
	}
}

func (r *responsesStreamState) addEvent(event responses.ResponseStreamEventUnion) (*corechat.ResponseDelta, error) {
	for _, pos := range []struct {
		name  string
		field respjson.Field
	}{
		{"output_index", event.JSON.OutputIndex}, {"content_index", event.JSON.ContentIndex}, {"summary_index", event.JSON.SummaryIndex}, {"annotation_index", event.JSON.AnnotationIndex},
	} {
		if pos.field.Raw() != "" {
			if _, valid := responseIndex(pos.field); !valid {
				return nil, fmt.Errorf("%w: openai responses: %s must be a nonnegative integer", corechat.ErrInvalidResponse, pos.name)
			}
		}
	}
	var part *corechat.PartDelta
	var err error
	switch typed := event.AsAny().(type) {
	case responses.ResponseCreatedEvent:
		return nil, r.bindResponseIdentity(typed.Response)
	case responses.ResponseOutputItemAddedEvent:
		if typed.Item.Type == responsesItemTypeMessage {
			return nil, r.content.startMessage(typed)
		}
		part, err = r.tools.add(typed)
	case responses.ResponseTextDeltaEvent:
		part, err = r.content.addText(typed)
		if err == nil && part != nil {
			r.lastText = &responsesContentPosition{output: typed.OutputIndex, content: typed.ContentIndex}
		}
	case responses.ResponseOutputTextAnnotationAddedEvent:
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
		position := responsesContentPosition{output: typed.OutputIndex, content: typed.ContentIndex}
		if evidence, known := r.content.messages[typed.OutputIndex].segments[typed.ContentIndex]; !known || evidence.kind != responsesContentTypeText {
			return nil, fmt.Errorf("%w: openai responses: annotation references unknown text", corechat.ErrInvalidResponse)
		}
		if r.lastText == nil || *r.lastText != position {
			return nil, fmt.Errorf("%w: openai responses: Core cannot attach a delayed annotation to an earlier text segment", errors.ErrUnsupported)
		}
		return r.deltaResponse(corechat.NewCitationDelta(citation))
	case responses.ResponseRefusalDeltaEvent:
		part, err = r.content.addRefusal(typed)
	case responses.ResponseFunctionCallArgumentsDeltaEvent:
		part, err = r.tools.appendArguments(typed)
	case responses.ResponseFunctionCallArgumentsDoneEvent:
		return nil, r.tools.finishArguments(typed)
	case responses.ResponseReasoningTextDeltaEvent:
		part, err = r.reasoning.delta(typed.OutputIndex, typed.JSON.OutputIndex.Valid() && typed.JSON.ContentIndex.Valid(), typed.Delta, responsesReasoningSegment{
			ItemID: typed.ItemID, Kind: responsesReasoningContent, Index: typed.ContentIndex,
		})
	case responses.ResponseReasoningSummaryTextDeltaEvent:
		part, err = r.reasoning.delta(typed.OutputIndex, typed.JSON.OutputIndex.Valid() && typed.JSON.SummaryIndex.Valid(), typed.Delta, responsesReasoningSegment{
			ItemID: typed.ItemID, Kind: responsesReasoningSummary, Index: typed.SummaryIndex,
		})
	case responses.ResponseReasoningSummaryPartAddedEvent:
		part, err = r.reasoning.delta(typed.OutputIndex, typed.JSON.OutputIndex.Valid() && typed.JSON.SummaryIndex.Valid(), typed.Part.Text, responsesReasoningSegment{
			ItemID: typed.ItemID, Kind: responsesReasoningSummary, Index: typed.SummaryIndex,
		})
	case responses.ResponseContentPartAddedEvent:
		if stateErr := r.content.messages[typed.OutputIndex].requireOpen(); stateErr != nil {
			return nil, stateErr
		}
		if typed.Part.Type != responsesReasoningContentText {
			return nil, nil
		}
		part, err = r.reasoning.delta(typed.OutputIndex, typed.JSON.OutputIndex.Valid() && typed.JSON.ContentIndex.Valid(), typed.Part.Text, responsesReasoningSegment{
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
	return r.deltaResponse(*part)
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
	part, err := r.reasoning.finish(event)
	if err != nil {
		return nil, err
	}
	return r.deltaResponse(*part)
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

func (r *responsesStreamState) deltaResponse(part corechat.PartDelta) (*corechat.ResponseDelta, error) {
	if part.Kind != corechat.PartDeltaText && part.Kind != corechat.PartDeltaCitation {
		r.lastText = nil
	}

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
