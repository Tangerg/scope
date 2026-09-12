package anthropic

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"

	corechat "github.com/Tangerg/scope/core/chat"
)

const (
	// ResponseExtensionKey preserves the complete official Anthropic response.
	ResponseExtensionKey = "anthropic/response"
	// StreamEventExtensionKey preserves each official Anthropic stream event.
	StreamEventExtensionKey = "anthropic/stream_event"
)

func mapProtocolMessage(message *anthropicsdk.Message, provider string) (*corechat.Response, error) {
	if message == nil {
		return nil, errors.New("anthropic: nil response")
	}
	parts, err := mapProtocolContent(message.Content, provider)
	if err != nil {
		return nil, err
	}
	output := &corechat.Output{
		FinishReason: normalizeProtocolStopReason(message.StopReason),
		Metadata:     &corechat.OutputMetadata{},
	}
	if err := output.Metadata.Extra.Set(protocolNativeStopReasonKey, message.StopReason); err != nil {
		return nil, err
	}
	if len(parts) > 0 {
		output.Message = &corechat.Message{Role: corechat.RoleAssistant, Parts: parts}
	}
	response := &corechat.Response{
		Output: output,
		Metadata: &corechat.ResponseMetadata{
			ID:    message.ID,
			Model: string(message.Model),
			Usage: mapProtocolUsage(message.Usage),
		},
	}
	if err := response.Metadata.Extra.Set(protocolResponseExtensionKey(provider), message); err != nil {
		return nil, err
	}
	if message.StopSequence != "" {
		if err := response.Metadata.Extra.Set(protocolStopSequenceKey, message.StopSequence); err != nil {
			return nil, err
		}
	}
	if err := response.Metadata.Extra.Set(protocolUsageKey, message.Usage); err != nil {
		return nil, err
	}
	if err := response.Validate(); err != nil {
		return nil, fmt.Errorf("anthropic: mapped response: %w", err)
	}
	return response, nil
}

func mapProtocolContent(blocks []anthropicsdk.ContentBlockUnion, provider string) ([]corechat.Part, error) {
	parts := make([]corechat.Part, 0, len(blocks))
	for i := range blocks {
		block := blocks[i]
		switch block.Type {
		case "text":
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
		case "thinking":
			if block.Thinking == "" && block.Signature == "" {
				return nil, fmt.Errorf("anthropic: content[%d]: empty thinking block", i)
			}
			part := corechat.NewReasoningPart(block.Thinking, []byte(block.Signature))
			if err := setProtocolReasoningState(&part, provider, protocolReasoningThinking); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case "redacted_thinking":
			if block.Data == "" {
				return nil, fmt.Errorf("anthropic: content[%d]: empty redacted thinking block", i)
			}
			part := corechat.NewReasoningPart("", []byte(block.Data))
			if err := setProtocolReasoningState(&part, provider, protocolReasoningRedacted); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case "tool_use":
			parts = append(parts, corechat.NewToolCallPart(corechat.ToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: string(block.Input),
			}))
		default:
			// Server-tool and future native blocks have no provider-neutral Core
			// part. The complete message remains available under
			// ResponseExtensionKey, so skipping them here is lossless.
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

func setProtocolReasoningDeltaState(part *corechat.PartDelta, provider, kind string) error {
	if err := part.Metadata.Set(protocolReasoningProviderKey, provider); err != nil {
		return fmt.Errorf("anthropic: preserve reasoning provider: %w", err)
	}
	if err := part.Metadata.Set(protocolReasoningKindKey, kind); err != nil {
		return fmt.Errorf("anthropic: preserve reasoning kind: %w", err)
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

type protocolStreamTool struct {
	id               string
	name             string
	pendingArguments string
}

type protocolStreamState struct {
	provider       string
	streamEventKey string
	id             string
	model          string
	tools          map[int64]protocolStreamTool
	usage          *corechat.Usage
	finish         corechat.FinishReason
}

func newProtocolStreamState(provider string) *protocolStreamState {
	return &protocolStreamState{
		provider:       provider,
		streamEventKey: protocolStreamEventExtensionKey(provider),
		tools:          make(map[int64]protocolStreamTool),
	}
}

func (p *protocolStreamState) mapEvent(event anthropicsdk.MessageStreamEventUnion) (*corechat.ResponseDelta, error) {
	response := &corechat.ResponseDelta{Metadata: &corechat.ResponseMetadata{ID: p.id, Model: p.model}}
	if err := response.Metadata.Extra.Set(p.streamEventKey, event); err != nil {
		return nil, err
	}
	switch value := event.AsAny().(type) {
	case anthropicsdk.MessageStartEvent:
		parts, err := p.mapMessageStart(value, response)
		if err != nil {
			return nil, err
		}
		response.Parts = parts
	case anthropicsdk.ContentBlockStartEvent:
		part, include, err := p.mapBlockStart(value)
		if err != nil {
			return nil, err
		}
		if include {
			response.Parts = []corechat.PartDelta{part}
		}
	case anthropicsdk.ContentBlockDeltaEvent:
		part, include, err := p.mapBlockDelta(value)
		if err != nil {
			return nil, err
		}
		if include {
			response.Parts = []corechat.PartDelta{part}
		}
	case anthropicsdk.MessageDeltaEvent:
		if err := p.mapMessageDelta(value, response); err != nil {
			return nil, err
		}
	case anthropicsdk.ContentBlockStopEvent, anthropicsdk.MessageStopEvent:
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
			return errors.New("anthropic: stream emitted more than one finish reason")
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
	return p.finish != ""
}

func (p *protocolStreamState) complete(delta *corechat.ResponseDelta) (*corechat.ResponseDelta, error) {
	if delta == nil || p.finish == "" {
		return nil, fmt.Errorf("anthropic: stream: %w: missing terminal response", corechat.ErrInvalidResponse)
	}
	// A Core tool-call delta cannot carry arguments without an id and a name,
	// so input_json_delta for a block whose content_block_start never arrived
	// is buffered instead of emitted. A buffer still held at the end is a tool
	// call the stream described and never identified; letting the terminal
	// response through would return something that looks whole while missing
	// the call, its arguments discarded.
	for _, index := range slices.Sorted(maps.Keys(p.tools)) {
		tool := p.tools[index]
		if tool.id != "" && tool.name != "" {
			continue
		}
		return nil, fmt.Errorf("anthropic: stream: %w: content block %d ended with id=%q name=%q and %d buffered argument byte(s), so the tool call cannot be reported",
			corechat.ErrInvalidResponse, index, tool.id, tool.name, len(tool.pendingArguments))
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
	case "text":
		if block.Text == "" {
			return corechat.PartDelta{}, false, nil
		}
		return corechat.NewTextDelta(block.Text), true, nil
	case "thinking":
		if block.Thinking == "" && block.Signature == "" {
			return corechat.PartDelta{}, false, nil
		}
		part := corechat.NewReasoningDelta(block.Thinking, []byte(block.Signature))
		if err := setProtocolReasoningDeltaState(&part, p.provider, protocolReasoningThinking); err != nil {
			return corechat.PartDelta{}, false, err
		}
		return part, true, nil
	case "redacted_thinking":
		if block.Data == "" {
			return corechat.PartDelta{}, false, errors.New("anthropic: empty redacted thinking block")
		}
		part := corechat.NewReasoningDelta("", []byte(block.Data))
		if err := setProtocolReasoningDeltaState(&part, p.provider, protocolReasoningRedacted); err != nil {
			return corechat.PartDelta{}, false, err
		}
		return part, true, nil
	case "tool_use":
		tool := p.tools[event.Index]
		tool.id = block.ID
		tool.name = block.Name
		p.tools[event.Index] = tool
		if tool.id == "" || tool.name == "" {
			return corechat.PartDelta{}, false, errors.New("anthropic: tool_use start requires ID and name")
		}
		arguments := tool.pendingArguments
		tool.pendingArguments = ""
		p.tools[event.Index] = tool
		return corechat.NewToolCallDelta(corechat.ToolCallDelta{ID: tool.id, Name: tool.name, Arguments: arguments}), true, nil
	default:
		return corechat.PartDelta{}, false, nil
	}
}

func (p *protocolStreamState) mapBlockDelta(event anthropicsdk.ContentBlockDeltaEvent) (corechat.PartDelta, bool, error) {
	switch delta := event.Delta.AsAny().(type) {
	case anthropicsdk.TextDelta:
		if delta.Text == "" {
			return corechat.PartDelta{}, false, nil
		}
		return corechat.NewTextDelta(delta.Text), true, nil
	case anthropicsdk.ThinkingDelta:
		if delta.Thinking == "" {
			return corechat.PartDelta{}, false, nil
		}
		part := corechat.NewReasoningDelta(delta.Thinking, nil)
		if err := setProtocolReasoningDeltaState(&part, p.provider, protocolReasoningThinking); err != nil {
			return corechat.PartDelta{}, false, err
		}
		return part, true, nil
	case anthropicsdk.SignatureDelta:
		if delta.Signature == "" {
			return corechat.PartDelta{}, false, nil
		}
		part := corechat.NewReasoningDelta("", []byte(delta.Signature))
		if err := setProtocolReasoningDeltaState(&part, p.provider, protocolReasoningThinking); err != nil {
			return corechat.PartDelta{}, false, err
		}
		return part, true, nil
	case anthropicsdk.InputJSONDelta:
		tool := p.tools[event.Index]
		tool.pendingArguments += delta.PartialJSON
		p.tools[event.Index] = tool
		if tool.id == "" || tool.name == "" {
			return corechat.PartDelta{}, false, nil
		}
		arguments := tool.pendingArguments
		tool.pendingArguments = ""
		p.tools[event.Index] = tool
		return corechat.NewToolCallDelta(corechat.ToolCallDelta{ID: tool.id, Name: tool.name, Arguments: arguments}), true, nil
	case anthropicsdk.CitationsDelta:
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
