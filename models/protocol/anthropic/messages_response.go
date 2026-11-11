package anthropic

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go/v2"

	corechat "github.com/Tangerg/scope/core/chat"
	corecontent "github.com/Tangerg/scope/core/content"
)

const (
	// StreamEventExtensionKey preserves each official Anthropic stream event.
	StreamEventExtensionKey = ProviderID + "/stream_event"

	protocolBlockText    = "text"
	protocolBlockToolUse = "tool_use"
)

func mapProtocolContent(blocks []anthropicsdk.ContentBlockUnion, provider string, documents protocolDocumentSources) ([]corechat.Part, error) {
	parts := make([]corechat.Part, 0, len(blocks))
	for i := range blocks {
		block := blocks[i]
		switch block.Type {
		case protocolBlockText:
			if block.Text == "" && len(block.Citations) != 0 {
				return nil, fmt.Errorf("%w: anthropic: content[%d]: citations require text in their block", corechat.ErrInvalidResponse, i)
			}
			if block.Text != "" {
				part := corechat.NewTextPart(block.Text)
				for citationIndex := range block.Citations {
					citation, include, citationErr := mapProtocolTextCitation(block.Citations[citationIndex], documents)
					if citationErr != nil {
						return nil, fmt.Errorf("anthropic: content[%d].citations[%d]: %w", i, citationIndex, citationErr)
					}
					if include {
						part.Citations = append(part.Citations, citation)
					}
				}
				parts = append(parts, part)
			}
		case string(ReasoningBlockThinking):
			if block.Thinking == "" && block.Signature == "" {
				return nil, fmt.Errorf("anthropic: content[%d]: empty thinking block", i)
			}
			part := corechat.NewReasoningPart(block.Thinking, []byte(block.Signature))
			if err := setProtocolReasoningState(&part, provider, ReasoningBlockThinking); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case string(ReasoningBlockRedacted):
			if block.Data == "" {
				return nil, fmt.Errorf("anthropic: content[%d]: empty redacted thinking block", i)
			}
			part := corechat.NewReasoningPart("", []byte(block.Data))
			if err := setProtocolReasoningState(&part, provider, ReasoningBlockRedacted); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case protocolBlockToolUse:
			parts = append(parts, corechat.NewToolCallPart(corechat.ToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: string(block.Input),
			}))
		default:
			return nil, fmt.Errorf("anthropic: content[%d]: %w: content block %q cannot be replayed", i, errors.ErrUnsupported, block.Type)
		}
	}
	return parts, nil
}

func normalizeProtocolStopReason(reason anthropicsdk.StopReason) (corechat.FinishReason, error) {
	switch reason {
	case "":
		return "", nil
	case anthropicsdk.StopReasonEndTurn, anthropicsdk.StopReasonStopSequence:
		return corechat.FinishReasonStop, nil
	case anthropicsdk.StopReasonMaxTokens, anthropicsdk.StopReasonModelContextWindowExceeded:
		return corechat.FinishReasonLength, nil
	case anthropicsdk.StopReasonToolUse:
		return corechat.FinishReasonToolCalls, nil
	case anthropicsdk.StopReasonRefusal:
		return corechat.FinishReasonRefusal, nil
	case anthropicsdk.StopReasonPauseTurn:
		// A paused server-tool loop requires native content and tools that this
		// binding cannot faithfully replay through Core messages.
		return "", fmt.Errorf("anthropic: %w: stop reason %q requires native server-tool continuation", errors.ErrUnsupported, reason)
	default:
		return corechat.FinishReasonOther, nil
	}
}

func setProtocolReasoningState(part *corechat.Part, provider string, kind ReasoningBlockKind) error {
	if err := part.Metadata.Set(protocolReasoningProviderKey, provider); err != nil {
		return fmt.Errorf("anthropic: preserve reasoning provider: %w", err)
	}
	if err := part.Metadata.Set(protocolReasoningKindKey, kind); err != nil {
		return fmt.Errorf("anthropic: preserve reasoning kind: %w", err)
	}
	return nil
}

func setProtocolReasoningDeltaState(part *corechat.PartDelta, provider string, kind ReasoningBlockKind) error {
	if err := part.Metadata.Set(protocolReasoningProviderKey, provider); err != nil {
		return fmt.Errorf("anthropic: preserve reasoning provider: %w", err)
	}
	if err := part.Metadata.Set(protocolReasoningKindKey, kind); err != nil {
		return fmt.Errorf("anthropic: preserve reasoning kind: %w", err)
	}
	return nil
}

func mapProtocolTextCitation(citation anthropicsdk.TextCitationUnion, documents protocolDocumentSources) (corecontent.Citation, bool, error) {
	return mapProtocolCitation(citation.AsAny(), documents)
}

func mapProtocolDeltaCitation(citation anthropicsdk.CitationsDeltaCitationUnion, documents protocolDocumentSources) (corecontent.Citation, bool, error) {
	return mapProtocolCitation(citation.AsAny(), documents)
}

func mapProtocolCitation(value any, documents protocolDocumentSources) (corecontent.Citation, bool, error) {
	switch citation := value.(type) {
	case anthropicsdk.CitationCharLocation:
		return documents.citation(citation.JSON.DocumentIndex.Raw(), citation.DocumentTitle, citation.CitedText)
	case anthropicsdk.CitationPageLocation:
		return documents.citation(citation.JSON.DocumentIndex.Raw(), citation.DocumentTitle, citation.CitedText)
	case anthropicsdk.CitationContentBlockLocation:
		return documents.citation(citation.JSON.DocumentIndex.Raw(), citation.DocumentTitle, citation.CitedText)
	case anthropicsdk.CitationsWebSearchResultLocation:
		return corecontent.Citation{
			Source: corecontent.CitationSource{Kind: corecontent.CitationSourceURI, Value: citation.URL},
			Title:  citation.Title,
			Quote:  citation.CitedText,
		}, true, nil
	case anthropicsdk.CitationsSearchResultLocation:
		if citation.Source == "" {
			return corecontent.Citation{}, false, errors.New("search result citation lacks source")
		}
		return corecontent.Citation{
			Source: corecontent.CitationSource{Kind: corecontent.CitationSourceReference, Value: citation.Source},
			Title:  citation.Title,
			Quote:  citation.CitedText,
		}, true, nil
	case nil:
		return corecontent.Citation{}, false, nil
	default:
		return corecontent.Citation{}, false, fmt.Errorf("unsupported citation %T", citation)
	}
}

type protocolDocumentSources []corecontent.CitationSource

func (p protocolDocumentSources) citation(rawIndex, title, quote string) (corecontent.Citation, bool, error) {
	index, err := strconv.ParseInt(rawIndex, 10, 64)
	if err != nil || index < 0 || index >= int64(len(p)) {
		return corecontent.Citation{}, false, fmt.Errorf("%w: anthropic: document_index %q does not identify a request document", corechat.ErrInvalidResponse, rawIndex)
	}
	return corecontent.Citation{
		Source: p[index],
		Title:  title,
		Quote:  quote,
	}, true, nil
}

type protocolStreamPhase uint8

const (
	protocolStreamAwaitingStart protocolStreamPhase = iota
	protocolStreamActive
	protocolStreamStopped
)

type protocolStreamBlock struct {
	kind     string
	open     bool
	toolID   string
	toolName string
}

func (p protocolStreamBlock) requireKind(kind string) error {
	if p.kind != kind {
		return fmt.Errorf("%w: delta requires %q block, got %q", corechat.ErrInvalidResponse, kind, p.kind)
	}
	return nil
}

type protocolStreamState struct {
	provider       string
	streamEventKey string
	id             string
	model          string
	documents      protocolDocumentSources
	blocks         map[int64]protocolStreamBlock
	usage          messagesUsage
	finish         corechat.FinishReason
	phase          protocolStreamPhase
}

func newProtocolStreamState(provider string, documents protocolDocumentSources) *protocolStreamState {
	return &protocolStreamState{
		provider:       provider,
		streamEventKey: protocolStreamEventExtensionKey(provider),
		documents:      documents,
		blocks:         make(map[int64]protocolStreamBlock),
	}
}

func (p *protocolStreamState) mapEvent(event anthropicsdk.MessageStreamEventUnion) (*corechat.ResponseDelta, error) {
	value := event.AsAny()
	if err := p.admit(event, value); err != nil {
		return nil, err
	}
	response := &corechat.ResponseDelta{Metadata: &corechat.ResponseMetadata{ID: p.id, Model: p.model}}
	if err := response.Metadata.Extra.Set(p.streamEventKey, json.RawMessage(event.RawJSON())); err != nil {
		return nil, err
	}
	switch value := value.(type) {
	case anthropicsdk.MessageStartEvent:
		if p.phase != protocolStreamAwaitingStart {
			return nil, fmt.Errorf("anthropic: stream: %w: more than one message_start", corechat.ErrInvalidResponse)
		}
		parts, err := p.mapMessageStart(value, response)
		if err != nil {
			return nil, err
		}
		response.Parts = parts
		p.phase = protocolStreamActive
	case anthropicsdk.ContentBlockStartEvent:
		parts, err := p.startBlock(value)
		if err != nil {
			return nil, err
		}
		response.Parts = parts
	case anthropicsdk.ContentBlockDeltaEvent:
		parts, err := p.extendBlock(value)
		if err != nil {
			return nil, err
		}
		response.Parts = parts
	case anthropicsdk.MessageDeltaEvent:
		if err := p.requireClosedBlocks(); err != nil {
			return nil, err
		}
		if err := p.mapMessageDelta(value, response); err != nil {
			return nil, err
		}
	case anthropicsdk.ContentBlockStopEvent:
		if err := p.stopBlock(value); err != nil {
			return nil, err
		}
	case anthropicsdk.MessageStopEvent:
		if p.finish == "" {
			return nil, fmt.Errorf("anthropic: stream: %w: message_stop without a finish reason", corechat.ErrInvalidResponse)
		}
		p.phase = protocolStreamStopped
	}
	usage, err := p.usage.snapshot()
	if err != nil {
		return nil, err
	}
	response.Metadata.Usage = usage
	if err := response.Validate(); err != nil {
		return nil, fmt.Errorf("anthropic: mapped stream response: %w", err)
	}
	return response, nil
}

// admit checks an event's wire shape and its place in the native lifecycle
// before any of it is mapped.
func (p *protocolStreamState) admit(event anthropicsdk.MessageStreamEventUnion, value any) error {
	var wireErr error
	if value == nil {
		wireErr = validateResponseWire[struct {
			Type responseWireString `json:"type"`
		}](event.RawJSON())
	} else {
		wireErr = validateResponseWire[responseEventWire](event.RawJSON())
	}
	if wireErr != nil {
		return fmt.Errorf("%w: anthropic: stream event: %w", corechat.ErrInvalidResponse, wireErr)
	}
	if p.phase == protocolStreamStopped {
		return fmt.Errorf("anthropic: stream: %w: event after message_stop", corechat.ErrInvalidResponse)
	}
	switch value.(type) {
	case anthropicsdk.ContentBlockStartEvent, anthropicsdk.ContentBlockDeltaEvent, anthropicsdk.ContentBlockStopEvent:
		// The SDK coerces missing, fractional and boolean positions to zero.
		// Never bind content or tool arguments to another block on that basis.
		index, err := strconv.ParseInt(event.JSON.Index.Raw(), 10, 64)
		if err != nil || index < 0 {
			return fmt.Errorf("%w: anthropic: %s requires a nonnegative integer index", corechat.ErrInvalidResponse, event.Type)
		}
	}
	_, startsMessage := value.(anthropicsdk.MessageStartEvent)
	if value != nil && !startsMessage && p.phase == protocolStreamAwaitingStart {
		return fmt.Errorf("anthropic: stream: %w: %s before message_start", corechat.ErrInvalidResponse, event.Type)
	}
	return nil
}

func (p *protocolStreamState) startBlock(event anthropicsdk.ContentBlockStartEvent) ([]corechat.PartDelta, error) {
	if p.finish != "" {
		return nil, fmt.Errorf("anthropic: stream: %w: content block %d started after the finish reason", corechat.ErrInvalidResponse, event.Index)
	}
	if _, exists := p.blocks[event.Index]; exists {
		return nil, fmt.Errorf("anthropic: stream: %w: content block %d started twice", corechat.ErrInvalidResponse, event.Index)
	}
	parts, err := p.mapBlockStart(event)
	if err != nil {
		return nil, err
	}
	block := protocolStreamBlock{kind: event.ContentBlock.Type, open: true}
	if block.kind == protocolBlockToolUse {
		block.toolID = event.ContentBlock.ID
		block.toolName = event.ContentBlock.Name
	}
	p.blocks[event.Index] = block
	return parts, nil
}

func (p *protocolStreamState) extendBlock(event anthropicsdk.ContentBlockDeltaEvent) ([]corechat.PartDelta, error) {
	if p.finish != "" {
		return nil, fmt.Errorf("anthropic: stream: %w: content block %d changed after the finish reason", corechat.ErrInvalidResponse, event.Index)
	}
	block, exists := p.blocks[event.Index]
	if !exists || !block.open {
		return nil, fmt.Errorf("anthropic: stream: %w: content block %d changed without an open block", corechat.ErrInvalidResponse, event.Index)
	}
	part, include, err := p.mapBlockDelta(event, block)
	if err != nil {
		return nil, fmt.Errorf("anthropic: stream: content block %d: %w", event.Index, err)
	}
	if !include {
		return nil, nil
	}
	part.Index = int(event.Index)
	return []corechat.PartDelta{part}, nil
}

func (p *protocolStreamState) stopBlock(event anthropicsdk.ContentBlockStopEvent) error {
	block := p.blocks[event.Index]
	if !block.open {
		return fmt.Errorf("anthropic: stream: %w: content block %d stopped without an open block", corechat.ErrInvalidResponse, event.Index)
	}
	block.open = false
	p.blocks[event.Index] = block
	return nil
}

func (p *protocolStreamState) requireClosedBlocks() error {
	for _, index := range slices.Sorted(maps.Keys(p.blocks)) {
		if p.blocks[index].open {
			return fmt.Errorf("anthropic: stream: %w: content block %d has no content_block_stop", corechat.ErrInvalidResponse, index)
		}
	}
	return nil
}

func (p *protocolStreamState) mapMessageStart(event anthropicsdk.MessageStartEvent, response *corechat.ResponseDelta) ([]corechat.PartDelta, error) {
	p.id = event.Message.ID
	p.model = string(event.Message.Model)
	response.Metadata.ID = p.id
	response.Metadata.Model = p.model
	if err := p.usage.observe(event.Message.JSON.Usage.Raw(), response.Metadata); err != nil {
		return nil, err
	}
	var deltas []corechat.PartDelta
	for index, block := range event.Message.Content {
		blockDeltas, err := p.mapContentBlock(block, index)
		if err != nil {
			return nil, err
		}
		deltas = append(deltas, blockDeltas...)
	}
	return deltas, nil
}

func (p *protocolStreamState) mapContentBlock(block anthropicsdk.ContentBlockUnion, index int) ([]corechat.PartDelta, error) {
	parts, err := mapProtocolContent([]anthropicsdk.ContentBlockUnion{block}, p.provider, p.documents)
	if err != nil {
		return nil, err
	}
	deltas, err := protocolPartsAsDeltas(parts)
	if err != nil {
		return nil, err
	}
	for position := range deltas {
		deltas[position].Index = index
	}
	return deltas, nil
}

func (p *protocolStreamState) mapMessageDelta(event anthropicsdk.MessageDeltaEvent, response *corechat.ResponseDelta) error {
	finish, err := normalizeProtocolStopReason(event.Delta.StopReason)
	if err != nil {
		return err
	}
	if finish != "" {
		if p.finish != "" {
			return fmt.Errorf("anthropic: stream: %w: more than one finish reason", corechat.ErrInvalidResponse)
		}
		p.finish = finish
	}
	if event.Delta.StopReason != "" {
		if err := response.Metadata.Extra.Set(protocolNativeStopReasonKey, event.Delta.StopReason); err != nil {
			return err
		}
	}
	if err := p.usage.observe(event.JSON.Usage.Raw(), response.Metadata); err != nil {
		return err
	}
	if event.Delta.StopSequence != "" {
		if err := response.Metadata.Extra.Set(protocolStopSequenceKey, event.Delta.StopSequence); err != nil {
			return err
		}
	}
	return nil
}

func (p *protocolStreamState) finished() bool {
	return p.phase == protocolStreamStopped
}

func (p *protocolStreamState) complete(delta *corechat.ResponseDelta) (*corechat.ResponseDelta, error) {
	if delta == nil || !p.finished() || p.finish == "" {
		return nil, fmt.Errorf("anthropic: stream: %w: missing terminal response", corechat.ErrInvalidResponse)
	}
	delta.FinishReason = p.finish
	if err := delta.Validate(); err != nil {
		return nil, fmt.Errorf("anthropic: terminal stream response: %w", err)
	}
	return delta, nil
}

func protocolPartsAsDeltas(parts []corechat.Part) ([]corechat.PartDelta, error) {
	deltas := make([]corechat.PartDelta, 0, len(parts))
	for index := range parts {
		part := parts[index]
		switch part.Kind {
		case corechat.PartText:
			delta := corechat.NewTextDelta(part.Text)
			delta.Metadata = part.Metadata.Clone()
			deltas = append(deltas, delta)
			for citationIndex := range part.Citations {
				deltas = append(deltas, corechat.NewCitationDelta(part.Citations[citationIndex]))
			}
		case corechat.PartReasoning:
			delta := corechat.NewReasoningDelta(part.Text, part.ReasoningState)
			delta.Metadata = part.Metadata.Clone()
			deltas = append(deltas, delta)
		case corechat.PartToolCall:
			delta := corechat.NewToolCallDelta(corechat.ToolCallDelta{
				ID: part.ToolCall.ID, Name: part.ToolCall.Name, Arguments: part.ToolCall.Arguments,
			})
			delta.Metadata = part.Metadata.Clone()
			deltas = append(deltas, delta)
		case corechat.PartRefusal:
			deltas = append(deltas, corechat.NewRefusalDelta(part.Text))
		default:
			return nil, fmt.Errorf("anthropic: stream content[%d]: unsupported part %q", index, part.Kind)
		}
	}
	return deltas, nil
}

func (p *protocolStreamState) mapBlockStart(event anthropicsdk.ContentBlockStartEvent) ([]corechat.PartDelta, error) {
	block := event.ContentBlock
	switch block.Type {
	case protocolBlockText:
		return p.mapContentBlock(anthropicsdk.ContentBlockUnion{Type: block.Type, Text: block.Text, Citations: block.Citations}, int(event.Index))
	case string(ReasoningBlockThinking):
		if block.Thinking == "" && block.Signature == "" {
			return nil, nil
		}
		part := corechat.NewReasoningDelta(block.Thinking, []byte(block.Signature))
		part.Index = int(event.Index)
		if err := setProtocolReasoningDeltaState(&part, p.provider, ReasoningBlockThinking); err != nil {
			return nil, err
		}
		return []corechat.PartDelta{part}, nil
	case string(ReasoningBlockRedacted):
		if block.Data == "" {
			return nil, errors.New("anthropic: empty redacted thinking block")
		}
		part := corechat.NewReasoningDelta("", []byte(block.Data))
		part.Index = int(event.Index)
		if err := setProtocolReasoningDeltaState(&part, p.provider, ReasoningBlockRedacted); err != nil {
			return nil, err
		}
		return []corechat.PartDelta{part}, nil
	case protocolBlockToolUse:
		for _, other := range p.blocks {
			if other.toolID == block.ID && block.ID != "" {
				return nil, fmt.Errorf("anthropic: stream: %w: tool id %q reused at content block %d", corechat.ErrInvalidResponse, block.ID, event.Index)
			}
		}
		if block.ID == "" || block.Name == "" {
			return nil, fmt.Errorf("anthropic: stream: %w: tool_use start requires ID and name", corechat.ErrInvalidResponse)
		}
		part := corechat.NewToolCallDelta(corechat.ToolCallDelta{ID: block.ID, Name: block.Name})
		part.Index = int(event.Index)
		return []corechat.PartDelta{part}, nil
	default:
		return nil, fmt.Errorf("anthropic: stream: %w: content block %q cannot be replayed", errors.ErrUnsupported, block.Type)
	}
}

func (p *protocolStreamState) mapBlockDelta(event anthropicsdk.ContentBlockDeltaEvent, block protocolStreamBlock) (corechat.PartDelta, bool, error) {
	switch delta := event.Delta.AsAny().(type) {
	case anthropicsdk.TextDelta:
		if err := block.requireKind(protocolBlockText); err != nil {
			return corechat.PartDelta{}, false, err
		}
		if delta.Text == "" {
			return corechat.PartDelta{}, false, nil
		}
		return corechat.NewTextDelta(delta.Text), true, nil
	case anthropicsdk.ThinkingDelta:
		if err := block.requireKind(string(ReasoningBlockThinking)); err != nil {
			return corechat.PartDelta{}, false, err
		}
		if delta.Thinking == "" {
			return corechat.PartDelta{}, false, nil
		}
		part := corechat.NewReasoningDelta(delta.Thinking, nil)
		if err := setProtocolReasoningDeltaState(&part, p.provider, ReasoningBlockThinking); err != nil {
			return corechat.PartDelta{}, false, err
		}
		return part, true, nil
	case anthropicsdk.SignatureDelta:
		if err := block.requireKind(string(ReasoningBlockThinking)); err != nil {
			return corechat.PartDelta{}, false, err
		}
		if delta.Signature == "" {
			return corechat.PartDelta{}, false, nil
		}
		part := corechat.NewReasoningDelta("", []byte(delta.Signature))
		if err := setProtocolReasoningDeltaState(&part, p.provider, ReasoningBlockThinking); err != nil {
			return corechat.PartDelta{}, false, err
		}
		return part, true, nil
	case anthropicsdk.InputJSONDelta:
		if err := block.requireKind(protocolBlockToolUse); err != nil {
			return corechat.PartDelta{}, false, err
		}
		return corechat.NewToolCallDelta(corechat.ToolCallDelta{ID: block.toolID, Name: block.toolName, Arguments: delta.PartialJSON}), true, nil
	case anthropicsdk.CitationsDelta:
		if err := block.requireKind(protocolBlockText); err != nil {
			return corechat.PartDelta{}, false, err
		}
		citation, include, err := mapProtocolDeltaCitation(delta.Citation, p.documents)
		if err != nil || !include {
			return corechat.PartDelta{}, false, err
		}
		return corechat.NewCitationDelta(citation), true, nil
	default:
		return corechat.PartDelta{}, false, fmt.Errorf("%w: content delta %q cannot be represented", errors.ErrUnsupported, event.Delta.Type)
	}
}
