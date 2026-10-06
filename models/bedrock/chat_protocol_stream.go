package bedrock

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/samber/lo"

	corechat "github.com/Tangerg/scope/core/chat"
)

type protocolBlockKind uint8

const (
	protocolTextBlock protocolBlockKind = iota + 1
	protocolReasoningTextBlock
	protocolReasoningRedactedBlock
	protocolToolBlock
)

type protocolStreamBlock struct {
	kind     protocolBlockKind
	open     bool
	toolID   string
	toolName string
}

type protocolChunkAccumulator struct {
	model            string
	blocks           map[int32]protocolStreamBlock
	started          bool
	stopReason       types.StopReason
	metadataReceived bool
}

func newProtocolChunkAccumulator(model string) *protocolChunkAccumulator {
	return &protocolChunkAccumulator{model: model, blocks: make(map[int32]protocolStreamBlock)}
}

// Only the native stop reason advances termination. A clean transport EOF can
// still truncate a message, while metadata follows messageStop on a whole one.
func (p *protocolChunkAccumulator) terminated() bool { return p.stopReason != "" }

func (p *protocolChunkAccumulator) add(event types.ConverseStreamOutput) (*corechat.ResponseDelta, bool, error) {
	if lo.IsNil(event) {
		return nil, false, fmt.Errorf("bedrock: stream: %w: missing event", corechat.ErrInvalidResponse)
	}
	if p.metadataReceived {
		return nil, false, fmt.Errorf("bedrock: stream: %w: event after metadata", corechat.ErrInvalidResponse)
	}
	if start, ok := event.(*types.ConverseStreamOutputMemberMessageStart); ok {
		if p.started || start.Value.Role != types.ConversationRoleAssistant {
			return nil, false, fmt.Errorf("bedrock: stream: %w: duplicate messageStart or non-assistant role", corechat.ErrInvalidResponse)
		}
		p.started = true
		return nil, false, nil
	}
	if !p.started {
		return nil, false, fmt.Errorf("bedrock: stream: %w: event before messageStart", corechat.ErrInvalidResponse)
	}
	if p.terminated() {
		if _, ok := event.(*types.ConverseStreamOutputMemberMetadata); !ok {
			return nil, false, fmt.Errorf("bedrock: stream: %w: event after messageStop", corechat.ErrInvalidResponse)
		}
	}

	response := &corechat.ResponseDelta{Metadata: &corechat.ResponseMetadata{Model: p.model}}
	var nativeValue any
	var nativeKey string
	switch typed := event.(type) {
	case *types.ConverseStreamOutputMemberContentBlockStart:
		index, err := protocolBlockIndex(typed.Value.ContentBlockIndex)
		if err != nil {
			return nil, false, err
		}
		if lo.IsNil(typed.Value.Start) {
			return nil, false, fmt.Errorf("bedrock: stream: %w: missing content block start payload", corechat.ErrInvalidResponse)
		}
		tool, ok := typed.Value.Start.(*types.ContentBlockStartMemberToolUse)
		if !ok {
			return nil, false, fmt.Errorf("bedrock: stream: %w: content block start %T", errors.ErrUnsupported, typed.Value.Start)
		}
		if tool.Value.ToolUseId == nil || *tool.Value.ToolUseId == "" || tool.Value.Name == nil || *tool.Value.Name == "" {
			return nil, false, fmt.Errorf("bedrock: stream: %w: toolUse content block opened without an id or name", corechat.ErrInvalidResponse)
		}
		if _, exists := p.blocks[index]; exists {
			return nil, false, fmt.Errorf("bedrock: stream: %w: content block %d started twice", corechat.ErrInvalidResponse, index)
		}
		for _, other := range p.blocks {
			if other.kind == protocolToolBlock && other.toolID == *tool.Value.ToolUseId {
				return nil, false, fmt.Errorf("bedrock: stream: %w: tool id %q reused at content block %d", corechat.ErrInvalidResponse, other.toolID, index)
			}
		}
		part := corechat.NewToolCallDelta(corechat.ToolCallDelta{ID: *tool.Value.ToolUseId, Name: *tool.Value.Name})
		if err := part.Validate(); err != nil {
			return nil, false, err
		}
		p.blocks[index] = protocolStreamBlock{kind: protocolToolBlock, open: true, toolID: *tool.Value.ToolUseId, toolName: *tool.Value.Name}
		response.Parts = []corechat.PartDelta{part}
	case *types.ConverseStreamOutputMemberContentBlockDelta:
		part, include, err := p.mapDelta(typed.Value)
		if err != nil || !include {
			return nil, false, err
		}
		response.Parts = []corechat.PartDelta{part}
	case *types.ConverseStreamOutputMemberContentBlockStop:
		index, err := protocolBlockIndex(typed.Value.ContentBlockIndex)
		if err != nil {
			return nil, false, err
		}
		block, exists := p.blocks[index]
		if !exists || !block.open {
			return nil, false, fmt.Errorf("bedrock: stream: %w: content block %d is not open", corechat.ErrInvalidResponse, index)
		}
		block.open = false
		p.blocks[index] = block
		return nil, false, nil
	case *types.ConverseStreamOutputMemberMessageStop:
		if typed.Value.StopReason == "" {
			return nil, false, fmt.Errorf("bedrock: stream: %w: missing stop reason", corechat.ErrInvalidResponse)
		}
		for index, block := range p.blocks {
			if block.open {
				return nil, false, fmt.Errorf("bedrock: stream: %w: content block %d is still open at messageStop", corechat.ErrInvalidResponse, index)
			}
		}
		nativeKey, nativeValue = ChatMessageStopExtensionKey, typed.Value
	case *types.ConverseStreamOutputMemberMetadata:
		if !p.terminated() {
			return nil, false, fmt.Errorf("bedrock: stream: %w: metadata before messageStop", corechat.ErrInvalidResponse)
		}
		response.Metadata.Usage = mapProtocolUsage(typed.Value.Usage)
		nativeKey, nativeValue = ChatMetadataExtensionKey, typed.Value
	default:
		return nil, false, fmt.Errorf("bedrock: stream: %w: event %T", errors.ErrUnsupported, event)
	}
	if nativeValue != nil {
		native, err := marshalProtocolJSON(nativeValue)
		if err != nil {
			return nil, false, fmt.Errorf("bedrock: encode stream metadata: %w", err)
		}
		if err := response.Metadata.Extra.Set(nativeKey, json.RawMessage(native)); err != nil {
			return nil, false, err
		}
	}
	if err := response.Validate(); err != nil {
		return nil, false, fmt.Errorf("bedrock: stream response: %w", err)
	}
	switch typed := event.(type) {
	case *types.ConverseStreamOutputMemberMessageStop:
		p.stopReason = typed.Value.StopReason
	case *types.ConverseStreamOutputMemberMetadata:
		p.metadataReceived = true
	}
	return response, true, nil
}

// complete derives termination on the final delta after the transport succeeds,
// so native usage and tail failures cannot arrive after a successful finish.
func (p *protocolChunkAccumulator) complete(delta *corechat.ResponseDelta) (*corechat.ResponseDelta, error) {
	if delta == nil || !p.terminated() {
		return nil, fmt.Errorf("bedrock: stream: %w: missing terminal response", corechat.ErrInvalidResponse)
	}
	delta.FinishReason = mapProtocolStopReason(p.stopReason)
	if delta.FinishReason == corechat.FinishReasonOther {
		delta.OutputMetadata = &corechat.OutputMetadata{}
		if err := delta.OutputMetadata.Extra.Set(chatNativeFinishReasonKey, string(p.stopReason)); err != nil {
			return nil, err
		}
	}
	if err := delta.Validate(); err != nil {
		return nil, fmt.Errorf("bedrock: terminal stream response: %w", err)
	}
	return delta, nil
}

func protocolBlockIndex(index *int32) (int32, error) {
	if index == nil || *index < 0 {
		return 0, fmt.Errorf("bedrock: stream: %w: missing or negative content block index", corechat.ErrInvalidResponse)
	}
	return *index, nil
}

func (p *protocolChunkAccumulator) mapDelta(delta types.ContentBlockDeltaEvent) (corechat.PartDelta, bool, error) {
	index, err := protocolBlockIndex(delta.ContentBlockIndex)
	if err != nil {
		return corechat.PartDelta{}, false, err
	}
	if lo.IsNil(delta.Delta) {
		return corechat.PartDelta{}, false, fmt.Errorf("bedrock: stream: %w: missing content block delta payload", corechat.ErrInvalidResponse)
	}
	var kind protocolBlockKind
	var part corechat.PartDelta
	switch value := delta.Delta.(type) {
	case *types.ContentBlockDeltaMemberText:
		kind = protocolTextBlock
		part = corechat.NewTextDelta(value.Value)
	case *types.ContentBlockDeltaMemberReasoningContent:
		if lo.IsNil(value.Value) {
			return corechat.PartDelta{}, false, fmt.Errorf("bedrock: stream: %w: missing reasoning delta payload", corechat.ErrInvalidResponse)
		}
		switch reasoning := value.Value.(type) {
		case *types.ReasoningContentBlockDeltaMemberText:
			kind = protocolReasoningTextBlock
			part = corechat.NewReasoningDelta(reasoning.Value, nil)
		case *types.ReasoningContentBlockDeltaMemberSignature:
			kind = protocolReasoningTextBlock
			part = corechat.NewReasoningDelta("", []byte(reasoning.Value))
		case *types.ReasoningContentBlockDeltaMemberRedactedContent:
			kind = protocolReasoningRedactedBlock
			part = corechat.NewReasoningDelta("", reasoning.Value)
		default:
			return corechat.PartDelta{}, false, fmt.Errorf("bedrock: stream: %w: reasoning delta %T", errors.ErrUnsupported, value.Value)
		}
		reasoningKind := chatReasoningText
		if kind == protocolReasoningRedactedBlock {
			reasoningKind = chatReasoningRedacted
		}
		if err := setReasoningDeltaState(&part, reasoningKind, index); err != nil {
			return corechat.PartDelta{}, false, err
		}
	case *types.ContentBlockDeltaMemberToolUse:
		kind = protocolToolBlock
		if value.Value.Input == nil {
			return corechat.PartDelta{}, false, fmt.Errorf("bedrock: stream: %w: missing tool input", corechat.ErrInvalidResponse)
		}
		block := p.blocks[index]
		part = corechat.NewToolCallDelta(corechat.ToolCallDelta{ID: block.toolID, Name: block.toolName, Arguments: *value.Value.Input})
	default:
		return corechat.PartDelta{}, false, fmt.Errorf("bedrock: stream: %w: content block delta %T", errors.ErrUnsupported, delta.Delta)
	}
	block, exists := p.blocks[index]
	if exists && (!block.open || block.kind != kind) || !exists && kind == protocolToolBlock {
		return corechat.PartDelta{}, false, fmt.Errorf("bedrock: stream: %w: delta for closed, mismatched, or unopened tool content block %d", corechat.ErrInvalidResponse, index)
	}
	include := part.Text != "" || len(part.ReasoningState) != 0 || part.ToolCall != nil && part.ToolCall.Arguments != ""
	if include {
		if err := part.Validate(); err != nil {
			return corechat.PartDelta{}, false, err
		}
	}
	if !exists {
		// Text and reasoning blocks have no native start event. Their first
		// delta owns opening even when its payload is empty.
		p.blocks[index] = protocolStreamBlock{kind: kind, open: true}
	}
	return part, include, nil
}
