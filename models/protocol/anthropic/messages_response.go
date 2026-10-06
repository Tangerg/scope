package anthropic

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"

	corechat "github.com/Tangerg/scope/core/chat"
)

const (
	// StreamEventExtensionKey preserves each official Anthropic stream event.
	StreamEventExtensionKey = "anthropic/stream_event"

	protocolBlockText             = "text"
	protocolBlockThinking         = "thinking"
	protocolBlockRedactedThinking = "redacted_thinking"
	protocolBlockToolUse          = "tool_use"
)

func mapProtocolContent(blocks []anthropicsdk.ContentBlockUnion, provider string) ([]corechat.Part, error) {
	parts := make([]corechat.Part, 0, len(blocks))
	for i := range blocks {
		block := blocks[i]
		switch block.Type {
		case protocolBlockText:
			if block.Text != "" {
				part := corechat.NewTextPart(block.Text)
				for citationIndex := range block.Citations {
					citation, include, citationErr := mapProtocolTextCitation(block.Citations[citationIndex])
					if citationErr != nil {
						return nil, fmt.Errorf("anthropic: content[%d].citations[%d]: %w", i, citationIndex, citationErr)
					}
					if include {
						part.Citations = append(part.Citations, citation)
					}
				}
				parts = append(parts, part)
			}
		case protocolBlockThinking:
			if block.Thinking == "" && block.Signature == "" {
				return nil, fmt.Errorf("anthropic: content[%d]: empty thinking block", i)
			}
			part := corechat.NewReasoningPart(block.Thinking, []byte(block.Signature))
			if err := setProtocolReasoningState(&part, provider, protocolReasoningThinking); err != nil {
				return nil, err
			}
			if err := part.Metadata.Set(protocolContentBlockIndexKey, i); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case protocolBlockRedactedThinking:
			if block.Data == "" {
				return nil, fmt.Errorf("anthropic: content[%d]: empty redacted thinking block", i)
			}
			part := corechat.NewReasoningPart("", []byte(block.Data))
			if err := setProtocolReasoningState(&part, provider, protocolReasoningRedacted); err != nil {
				return nil, err
			}
			if err := part.Metadata.Set(protocolContentBlockIndexKey, i); err != nil {
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
			// Server-tool and future native blocks have no provider-neutral Core
			// part. The source event remains available under
			// StreamEventExtensionKey, so skipping them here is lossless.
			continue
		}
	}
	return parts, nil
}

func mapProtocolUsage(usage anthropicsdk.Usage) *corechat.Usage {
	if usage.RawJSON() != "" {
		if !usage.JSON.InputTokens.Valid() || !usage.JSON.OutputTokens.Valid() {
			return nil
		}
	} else if usage.InputTokens == 0 && usage.OutputTokens == 0 && usage.CacheReadInputTokens == 0 && usage.CacheCreationInputTokens == 0 {
		return nil
	}

	mapped := corechat.Usage{
		InputTokens:  protocolTotalInputTokens(usage.InputTokens, usage.CacheReadInputTokens, usage.CacheCreationInputTokens),
		OutputTokens: usage.OutputTokens,
	}
	if usage.OutputTokensDetails.JSON.ThinkingTokens.Valid() || usage.OutputTokensDetails.ThinkingTokens != 0 {
		value := usage.OutputTokensDetails.ThinkingTokens
		mapped.ReasoningTokens = &value
	}
	if usage.CacheReadInputTokens != 0 || usage.JSON.CacheReadInputTokens.Valid() {
		value := usage.CacheReadInputTokens
		mapped.CacheReadInputTokens = &value
	}
	if usage.CacheCreationInputTokens != 0 || usage.JSON.CacheCreationInputTokens.Valid() {
		value := usage.CacheCreationInputTokens
		mapped.CacheWriteInputTokens = &value
	}
	return &mapped
}

func protocolTotalInputTokens(uncached, cacheRead, cacheWrite int64) int64 {
	// Anthropic reports fresh, cache-read, and cache-write input as disjoint
	// counters. Core InputTokens is the total whose optional cache fields are
	// breakdowns, so normalize instead of copying the similarly named field.
	return uncached + cacheRead + cacheWrite
}

func normalizeProtocolStopReason(reason anthropicsdk.StopReason) corechat.FinishReason {
	switch reason {
	case "":
		return ""
	case anthropicsdk.StopReasonEndTurn, anthropicsdk.StopReasonStopSequence:
		return corechat.FinishReasonStop
	// All three leave a half-finished output that the caller continues to
	// finish, which is what FinishReasonLength groups by. max_tokens is the
	// budget the caller set; model_context_window_exceeded is the model's own
	// window filling first, which Anthropic tells clients to "treat as
	// truncated" all the same; and of pause_turn Anthropic says "we paused a
	// long-running turn. You may provide the response back as-is in a
	// subsequent request to let the model continue" — incomplete, with
	// continuation as the remedy. Answering Other for a paused turn would hide
	// it from every caller that decides whether to continue by reading the
	// finish reason. The exact reason stays on the output either way.
	case anthropicsdk.StopReasonMaxTokens,
		anthropicsdk.StopReasonModelContextWindowExceeded,
		anthropicsdk.StopReasonPauseTurn:
		return corechat.FinishReasonLength
	case anthropicsdk.StopReasonToolUse:
		return corechat.FinishReasonToolCalls
	case anthropicsdk.StopReasonRefusal:
		return corechat.FinishReasonRefusal
	default:
		return corechat.FinishReasonOther
	}
}

func setProtocolReasoningState(part *corechat.Part, provider, kind string) error {
	if err := part.Metadata.Set(protocolReasoningProviderKey, provider); err != nil {
		return fmt.Errorf("anthropic: preserve reasoning provider: %w", err)
	}
	if err := part.Metadata.Set(protocolReasoningKindKey, kind); err != nil {
		return fmt.Errorf("anthropic: preserve reasoning kind: %w", err)
	}
	return nil
}

func setProtocolReasoningDeltaState(part *corechat.PartDelta, provider, kind string, index int64) error {
	if err := part.Metadata.Set(protocolReasoningProviderKey, provider); err != nil {
		return fmt.Errorf("anthropic: preserve reasoning provider: %w", err)
	}
	if err := part.Metadata.Set(protocolReasoningKindKey, kind); err != nil {
		return fmt.Errorf("anthropic: preserve reasoning kind: %w", err)
	}
	// A signature protects one provider block. The index lets Core join its
	// fragments without concatenating independent signed or redacted blocks.
	if err := part.Metadata.Set(protocolContentBlockIndexKey, index); err != nil {
		return fmt.Errorf("anthropic: preserve reasoning content block index: %w", err)
	}
	return nil
}

func mapProtocolTextCitation(citation anthropicsdk.TextCitationUnion) (corechat.Citation, bool, error) {
	return mapProtocolCitation(citation.AsAny())
}

func mapProtocolDeltaCitation(citation anthropicsdk.CitationsDeltaCitationUnion) (corechat.Citation, bool, error) {
	return mapProtocolCitation(citation.AsAny())
}

func mapProtocolCitation(value any) (corechat.Citation, bool, error) {
	switch citation := value.(type) {
	case anthropicsdk.CitationCharLocation:
		return protocolDocumentCitation(citation.FileID, citation.DocumentTitle, citation.CitedText)
	case anthropicsdk.CitationPageLocation:
		return protocolDocumentCitation(citation.FileID, citation.DocumentTitle, citation.CitedText)
	case anthropicsdk.CitationContentBlockLocation:
		return protocolDocumentCitation(citation.FileID, citation.DocumentTitle, citation.CitedText)
	case anthropicsdk.CitationsWebSearchResultLocation:
		return corechat.Citation{
			Source: corechat.CitationSource{Kind: corechat.CitationSourceURI, Value: citation.URL},
			Title:  citation.Title,
			Quote:  citation.CitedText,
		}, true, nil
	case anthropicsdk.CitationsSearchResultLocation:
		if citation.Source == "" {
			return corechat.Citation{}, false, errors.New("search result citation lacks source")
		}
		return corechat.Citation{
			Source: corechat.CitationSource{Kind: corechat.CitationSourceReference, Value: citation.Source},
			Title:  citation.Title,
			Quote:  citation.CitedText,
		}, true, nil
	case nil:
		return corechat.Citation{}, false, nil
	default:
		return corechat.Citation{}, false, fmt.Errorf("unsupported citation %T", citation)
	}
}

func protocolDocumentCitation(fileID, title, quote string) (corechat.Citation, bool, error) {
	reference := fileID
	if reference == "" {
		reference = title
	}
	if reference == "" {
		return corechat.Citation{}, false, errors.New("document citation lacks an identity")
	}
	return corechat.Citation{
		Source: corechat.CitationSource{Kind: corechat.CitationSourceReference, Value: reference},
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
	blocks         map[int64]protocolStreamBlock
	usage          *corechat.Usage
	finish         corechat.FinishReason
	phase          protocolStreamPhase
}

func newProtocolStreamState(provider string) *protocolStreamState {
	return &protocolStreamState{
		provider:       provider,
		streamEventKey: protocolStreamEventExtensionKey(provider),
		blocks:         make(map[int64]protocolStreamBlock),
	}
}

func (p *protocolStreamState) mapEvent(event anthropicsdk.MessageStreamEventUnion) (*corechat.ResponseDelta, error) {
	if p.phase == protocolStreamStopped {
		return nil, fmt.Errorf("anthropic: stream: %w: event after message_stop", corechat.ErrInvalidResponse)
	}
	value := event.AsAny()
	_, startsMessage := value.(anthropicsdk.MessageStartEvent)
	if value != nil && !startsMessage && p.phase == protocolStreamAwaitingStart {
		return nil, fmt.Errorf("anthropic: stream: %w: %s before message_start", corechat.ErrInvalidResponse, event.Type)
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
		if p.finish != "" {
			return nil, fmt.Errorf("anthropic: stream: %w: content block %d started after the finish reason", corechat.ErrInvalidResponse, value.Index)
		}
		if _, exists := p.blocks[value.Index]; exists {
			return nil, fmt.Errorf("anthropic: stream: %w: content block %d started twice", corechat.ErrInvalidResponse, value.Index)
		}
		if value.Index < 0 {
			return nil, fmt.Errorf("anthropic: stream: %w: negative content block index %d", corechat.ErrInvalidResponse, value.Index)
		}
		part, include, err := p.mapBlockStart(value)
		if err != nil {
			return nil, err
		}
		if include {
			response.Parts = []corechat.PartDelta{part}
		}
		block := protocolStreamBlock{kind: value.ContentBlock.Type, open: true}
		if block.kind == protocolBlockToolUse {
			block.toolID = value.ContentBlock.ID
			block.toolName = value.ContentBlock.Name
		}
		p.blocks[value.Index] = block
	case anthropicsdk.ContentBlockDeltaEvent:
		if p.finish != "" {
			return nil, fmt.Errorf("anthropic: stream: %w: content block %d changed after the finish reason", corechat.ErrInvalidResponse, value.Index)
		}
		block, exists := p.blocks[value.Index]
		if !exists || !block.open {
			return nil, fmt.Errorf("anthropic: stream: %w: content block %d changed without an open block", corechat.ErrInvalidResponse, value.Index)
		}
		part, include, err := p.mapBlockDelta(value, block)
		if err != nil {
			return nil, fmt.Errorf("anthropic: stream: content block %d: %w", value.Index, err)
		}
		if include {
			response.Parts = []corechat.PartDelta{part}
		}
	case anthropicsdk.MessageDeltaEvent:
		for _, index := range slices.Sorted(maps.Keys(p.blocks)) {
			if p.blocks[index].open {
				return nil, fmt.Errorf("anthropic: stream: %w: content block %d has no content_block_stop", corechat.ErrInvalidResponse, index)
			}
		}
		if err := p.mapMessageDelta(value, response); err != nil {
			return nil, err
		}
	case anthropicsdk.ContentBlockStopEvent:
		block := p.blocks[value.Index]
		if !block.open {
			return nil, fmt.Errorf("anthropic: stream: %w: content block %d stopped without an open block", corechat.ErrInvalidResponse, value.Index)
		}
		block.open = false
		p.blocks[value.Index] = block
	case anthropicsdk.MessageStopEvent:
		if p.finish == "" {
			return nil, fmt.Errorf("anthropic: stream: %w: message_stop without a finish reason", corechat.ErrInvalidResponse)
		}
		p.phase = protocolStreamStopped
	}
	if p.usage != nil {
		response.Metadata.Usage = new(*p.usage)
	}
	if err := response.Validate(); err != nil {
		return nil, fmt.Errorf("anthropic: mapped stream response: %w", err)
	}
	return response, nil
}

func (p *protocolStreamState) mapMessageStart(event anthropicsdk.MessageStartEvent, response *corechat.ResponseDelta) ([]corechat.PartDelta, error) {
	p.id = event.Message.ID
	p.model = string(event.Message.Model)
	response.Metadata.ID = p.id
	response.Metadata.Model = p.model
	p.usage = mapProtocolUsage(event.Message.Usage)
	if err := response.Metadata.Extra.Set(protocolUsageKey, event.Message.Usage); err != nil {
		return nil, err
	}
	parts, err := mapProtocolContent(event.Message.Content, p.provider)
	if err != nil {
		return nil, err
	}
	return protocolPartsAsDeltas(parts)
}

func (p *protocolStreamState) mapMessageDelta(event anthropicsdk.MessageDeltaEvent, response *corechat.ResponseDelta) error {
	finish := normalizeProtocolStopReason(event.Delta.StopReason)
	if finish != "" {
		if p.finish != "" {
			return fmt.Errorf("anthropic: stream: %w: more than one finish reason", corechat.ErrInvalidResponse)
		}
		p.finish = finish
	}
	if event.Delta.StopReason != "" {
		response.OutputMetadata = &corechat.OutputMetadata{}
		if err := response.OutputMetadata.Extra.Set(protocolNativeStopReasonKey, event.Delta.StopReason); err != nil {
			return err
		}
	}
	p.mergeDeltaUsage(event.Usage)
	if err := response.Metadata.Extra.Set(protocolUsageKey, event.Usage); err != nil {
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

func (p *protocolStreamState) mapBlockStart(event anthropicsdk.ContentBlockStartEvent) (corechat.PartDelta, bool, error) {
	block := event.ContentBlock
	switch block.Type {
	case protocolBlockText:
		if block.Text == "" {
			return corechat.PartDelta{}, false, nil
		}
		return corechat.NewTextDelta(block.Text), true, nil
	case protocolBlockThinking:
		if block.Thinking == "" && block.Signature == "" {
			return corechat.PartDelta{}, false, nil
		}
		part := corechat.NewReasoningDelta(block.Thinking, []byte(block.Signature))
		if err := setProtocolReasoningDeltaState(&part, p.provider, protocolReasoningThinking, event.Index); err != nil {
			return corechat.PartDelta{}, false, err
		}
		return part, true, nil
	case protocolBlockRedactedThinking:
		if block.Data == "" {
			return corechat.PartDelta{}, false, errors.New("anthropic: empty redacted thinking block")
		}
		part := corechat.NewReasoningDelta("", []byte(block.Data))
		if err := setProtocolReasoningDeltaState(&part, p.provider, protocolReasoningRedacted, event.Index); err != nil {
			return corechat.PartDelta{}, false, err
		}
		return part, true, nil
	case protocolBlockToolUse:
		for _, other := range p.blocks {
			if other.toolID == block.ID && block.ID != "" {
				return corechat.PartDelta{}, false, fmt.Errorf("anthropic: stream: %w: tool id %q reused at content block %d", corechat.ErrInvalidResponse, block.ID, event.Index)
			}
		}
		if block.ID == "" || block.Name == "" {
			return corechat.PartDelta{}, false, fmt.Errorf("anthropic: stream: %w: tool_use start requires ID and name", corechat.ErrInvalidResponse)
		}
		return corechat.NewToolCallDelta(corechat.ToolCallDelta{ID: block.ID, Name: block.Name}), true, nil
	default:
		return corechat.PartDelta{}, false, nil
	}
}

func (p *protocolStreamState) mapBlockDelta(event anthropicsdk.ContentBlockDeltaEvent, block protocolStreamBlock) (corechat.PartDelta, bool, error) {
	switch block.kind {
	case protocolBlockText, protocolBlockThinking, protocolBlockRedactedThinking, protocolBlockToolUse:
	default:
		// Native server-tool and future blocks remain in event metadata; their
		// deltas cannot originate local Tool calls or visible Core content.
		return corechat.PartDelta{}, false, nil
	}
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
		if err := block.requireKind(protocolBlockThinking); err != nil {
			return corechat.PartDelta{}, false, err
		}
		if delta.Thinking == "" {
			return corechat.PartDelta{}, false, nil
		}
		part := corechat.NewReasoningDelta(delta.Thinking, nil)
		if err := setProtocolReasoningDeltaState(&part, p.provider, protocolReasoningThinking, event.Index); err != nil {
			return corechat.PartDelta{}, false, err
		}
		return part, true, nil
	case anthropicsdk.SignatureDelta:
		if err := block.requireKind(protocolBlockThinking); err != nil {
			return corechat.PartDelta{}, false, err
		}
		if delta.Signature == "" {
			return corechat.PartDelta{}, false, nil
		}
		part := corechat.NewReasoningDelta("", []byte(delta.Signature))
		if err := setProtocolReasoningDeltaState(&part, p.provider, protocolReasoningThinking, event.Index); err != nil {
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
		citation, include, err := mapProtocolDeltaCitation(delta.Citation)
		if err != nil || !include {
			return corechat.PartDelta{}, false, err
		}
		return corechat.NewCitationDelta(citation), true, nil
	default:
		return corechat.PartDelta{}, false, nil
	}
}

func (p *protocolStreamState) mergeDeltaUsage(usage anthropicsdk.MessageDeltaUsage) {
	if p.usage == nil {
		if !usage.JSON.InputTokens.Valid() || !usage.JSON.OutputTokens.Valid() {
			return
		}
		p.usage = &corechat.Usage{}
	}
	uncached := p.usage.InputTokens
	if p.usage.CacheReadInputTokens != nil {
		uncached -= *p.usage.CacheReadInputTokens
	}
	if p.usage.CacheWriteInputTokens != nil {
		uncached -= *p.usage.CacheWriteInputTokens
	}
	if usage.JSON.InputTokens.Valid() || usage.InputTokens != 0 {
		uncached = usage.InputTokens
	}
	if usage.JSON.OutputTokens.Valid() || usage.RawJSON() == "" {
		p.usage.OutputTokens = usage.OutputTokens
	}
	if usage.OutputTokensDetails.ThinkingTokens != 0 || usage.OutputTokensDetails.JSON.ThinkingTokens.Valid() {
		p.usage.ReasoningTokens = new(usage.OutputTokensDetails.ThinkingTokens)
	}
	if usage.CacheReadInputTokens != 0 || usage.JSON.CacheReadInputTokens.Valid() {
		p.usage.CacheReadInputTokens = new(usage.CacheReadInputTokens)
	}
	if usage.CacheCreationInputTokens != 0 || usage.JSON.CacheCreationInputTokens.Valid() {
		p.usage.CacheWriteInputTokens = new(usage.CacheCreationInputTokens)
	}
	p.usage.InputTokens = uncached
	if p.usage.CacheReadInputTokens != nil {
		p.usage.InputTokens += *p.usage.CacheReadInputTokens
	}
	if p.usage.CacheWriteInputTokens != nil {
		p.usage.InputTokens += *p.usage.CacheWriteInputTokens
	}
}
