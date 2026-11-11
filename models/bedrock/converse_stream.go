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

type conversePhase uint8

const (
	converseAwaitingStart conversePhase = iota
	converseStreaming
	converseStopped
	converseMetadataReceived
)

type converseStream struct {
	model      string
	blocks     map[int32]protocolStreamBlock
	phase      conversePhase
	stopReason types.StopReason
}

func newConverseStream(model string) *converseStream {
	return &converseStream{model: model, blocks: make(map[int32]protocolStreamBlock)}
}

// Transport EOF cannot advance the native lifecycle: messageStop must arrive
// before trailing metadata or final completion.
func (c *converseStream) terminated() bool {
	return c.phase == converseStopped || c.phase == converseMetadataReceived
}

func (c *converseStream) add(event types.ConverseStreamOutput) (*corechat.ResponseDelta, bool, error) {
	if lo.IsNil(event) {
		return nil, false, fmt.Errorf("bedrock: stream: %w: missing event", corechat.ErrInvalidResponse)
	}
	if c.phase == converseMetadataReceived {
		return nil, false, fmt.Errorf("bedrock: stream: %w: event after metadata", corechat.ErrInvalidResponse)
	}
	if start, ok := event.(*types.ConverseStreamOutputMemberMessageStart); ok {
		if c.phase != converseAwaitingStart || start.Value.Role != types.ConversationRoleAssistant {
			return nil, false, fmt.Errorf("bedrock: stream: %w: duplicate messageStart or non-assistant role", corechat.ErrInvalidResponse)
		}
		c.phase = converseStreaming
		return nil, false, nil
	}
	if c.phase == converseAwaitingStart {
		return nil, false, fmt.Errorf("bedrock: stream: %w: event before messageStart", corechat.ErrInvalidResponse)
	}
	if c.terminated() {
		if _, ok := event.(*types.ConverseStreamOutputMemberMetadata); !ok {
			return nil, false, fmt.Errorf("bedrock: stream: %w: event after messageStop", corechat.ErrInvalidResponse)
		}
	}

	response := &corechat.ResponseDelta{Metadata: &corechat.ResponseMetadata{Model: c.model}}
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
		if tool.Value.Type != "" {
			return nil, false, fmt.Errorf("bedrock: stream: %w: tool use type %q", errors.ErrUnsupported, tool.Value.Type)
		}
		if tool.Value.ToolUseId == nil || *tool.Value.ToolUseId == "" || tool.Value.Name == nil || *tool.Value.Name == "" {
			return nil, false, fmt.Errorf("bedrock: stream: %w: toolUse content block opened without an id or name", corechat.ErrInvalidResponse)
		}
		if _, exists := c.blocks[index]; exists {
			return nil, false, fmt.Errorf("bedrock: stream: %w: content block %d started twice", corechat.ErrInvalidResponse, index)
		}
		for _, other := range c.blocks {
			if other.kind == protocolToolBlock && other.toolID == *tool.Value.ToolUseId {
				return nil, false, fmt.Errorf("bedrock: stream: %w: tool id %q reused at content block %d", corechat.ErrInvalidResponse, other.toolID, index)
			}
		}
		part := corechat.NewToolCallDelta(corechat.ToolCallDelta{ID: *tool.Value.ToolUseId, Name: *tool.Value.Name})
		part.Index = int(index)
		if err := part.Validate(); err != nil {
			return nil, false, err
		}
		c.blocks[index] = protocolStreamBlock{kind: protocolToolBlock, open: true, toolID: *tool.Value.ToolUseId, toolName: *tool.Value.Name}
		response.Parts = []corechat.PartDelta{part}
	case *types.ConverseStreamOutputMemberContentBlockDelta:
		part, include, err := c.mapDelta(typed.Value)
		if err != nil || !include {
			return nil, false, err
		}
		response.Parts = []corechat.PartDelta{part}
	case *types.ConverseStreamOutputMemberContentBlockStop:
		index, err := protocolBlockIndex(typed.Value.ContentBlockIndex)
		if err != nil {
			return nil, false, err
		}
		block, exists := c.blocks[index]
		if !exists || !block.open {
			return nil, false, fmt.Errorf("bedrock: stream: %w: content block %d is not open", corechat.ErrInvalidResponse, index)
		}
		block.open = false
		c.blocks[index] = block
		return nil, false, nil
	case *types.ConverseStreamOutputMemberMessageStop:
		if typed.Value.StopReason == "" {
			return nil, false, fmt.Errorf("bedrock: stream: %w: missing stop reason", corechat.ErrInvalidResponse)
		}
		for index, block := range c.blocks {
			if block.open {
				return nil, false, fmt.Errorf("bedrock: stream: %w: content block %d is still open at messageStop", corechat.ErrInvalidResponse, index)
			}
		}
		nativeKey, nativeValue = ChatMessageStopExtensionKey, typed.Value
	case *types.ConverseStreamOutputMemberMetadata:
		if !c.terminated() {
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
		c.stopReason = typed.Value.StopReason
		c.phase = converseStopped
	case *types.ConverseStreamOutputMemberMetadata:
		c.phase = converseMetadataReceived
	}
	return response, true, nil
}

// complete derives termination on the final delta after the transport succeeds,
// so native usage and tail failures cannot arrive after a successful finish.
func (c *converseStream) complete(delta *corechat.ResponseDelta) (*corechat.ResponseDelta, error) {
	if delta == nil || !c.terminated() {
		return nil, fmt.Errorf("bedrock: stream: %w: missing terminal response", corechat.ErrInvalidResponse)
	}
	delta.FinishReason = mapProtocolStopReason(c.stopReason)
	if delta.FinishReason == corechat.FinishReasonOther {
		if err := delta.Metadata.Extra.Set(chatNativeFinishReasonKey, string(c.stopReason)); err != nil {
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

func (c *converseStream) mapDelta(delta types.ContentBlockDeltaEvent) (corechat.PartDelta, bool, error) {
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
		if err := setReasoningDeltaState(&part, reasoningKind); err != nil {
			return corechat.PartDelta{}, false, err
		}
	case *types.ContentBlockDeltaMemberToolUse:
		kind = protocolToolBlock
		if value.Value.Input == nil {
			return corechat.PartDelta{}, false, fmt.Errorf("bedrock: stream: %w: missing tool input", corechat.ErrInvalidResponse)
		}
		block := c.blocks[index]
		part = corechat.NewToolCallDelta(corechat.ToolCallDelta{ID: block.toolID, Name: block.toolName, Arguments: *value.Value.Input})
	default:
		return corechat.PartDelta{}, false, fmt.Errorf("bedrock: stream: %w: content block delta %T", errors.ErrUnsupported, delta.Delta)
	}
	block, exists := c.blocks[index]
	if exists && (!block.open || block.kind != kind) || !exists && kind == protocolToolBlock {
		return corechat.PartDelta{}, false, fmt.Errorf("bedrock: stream: %w: delta for closed, mismatched, or unopened tool content block %d", corechat.ErrInvalidResponse, index)
	}
	part.Index = int(index)
	include := part.Text != "" || len(part.ReasoningState) != 0 || part.ToolCall != nil && part.ToolCall.Arguments != ""
	if include {
		if err := part.Validate(); err != nil {
			return corechat.PartDelta{}, false, err
		}
	}
	if !exists {
		// Text and reasoning blocks have no native start event. Their first
		// delta owns opening even when its payload is empty.
		c.blocks[index] = protocolStreamBlock{kind: kind, open: true}
	}
	return part, include, nil
}
