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
	if started, err := c.admit(event); started || err != nil {
		return nil, false, err
	}
	response := &corechat.ResponseDelta{Metadata: &corechat.ResponseMetadata{Model: c.model}}
	var nativeValue any
	var nativeKey string
	switch typed := event.(type) {
	case *types.ConverseStreamOutputMemberContentBlockStart:
		part, err := c.startBlock(typed.Value)
		if err != nil {
			return nil, false, err
		}
		response.Parts = []corechat.PartDelta{part}
	case *types.ConverseStreamOutputMemberContentBlockDelta:
		part, include, err := c.mapDelta(typed.Value)
		if err != nil || !include {
			return nil, false, err
		}
		response.Parts = []corechat.PartDelta{part}
	case *types.ConverseStreamOutputMemberContentBlockStop:
		return nil, false, c.stopBlock(typed.Value)
	case *types.ConverseStreamOutputMemberMessageStop:
		if err := c.checkMessageStop(typed.Value); err != nil {
			return nil, false, err
		}
		nativeKey, nativeValue = ChatMessageStopExtensionKey, typed.Value
	case *types.ConverseStreamOutputMemberMetadata:
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

// admit enforces the native event order: messageStart first, content until
// messageStop, then only trailing metadata. It reports a consumed messageStart.
func (c *converseStream) admit(event types.ConverseStreamOutput) (bool, error) {
	if lo.IsNil(event) {
		return false, fmt.Errorf("bedrock: stream: %w: missing event", corechat.ErrInvalidResponse)
	}
	if c.phase == converseMetadataReceived {
		return false, fmt.Errorf("bedrock: stream: %w: event after metadata", corechat.ErrInvalidResponse)
	}
	if start, ok := event.(*types.ConverseStreamOutputMemberMessageStart); ok {
		if c.phase != converseAwaitingStart || start.Value.Role != types.ConversationRoleAssistant {
			return false, fmt.Errorf("bedrock: stream: %w: duplicate messageStart or non-assistant role", corechat.ErrInvalidResponse)
		}
		c.phase = converseStreaming
		return true, nil
	}
	_, metadata := event.(*types.ConverseStreamOutputMemberMetadata)
	switch {
	case c.phase == converseAwaitingStart:
		return false, fmt.Errorf("bedrock: stream: %w: event before messageStart", corechat.ErrInvalidResponse)
	case c.terminated() && !metadata:
		return false, fmt.Errorf("bedrock: stream: %w: event after messageStop", corechat.ErrInvalidResponse)
	case !c.terminated() && metadata:
		return false, fmt.Errorf("bedrock: stream: %w: metadata before messageStop", corechat.ErrInvalidResponse)
	default:
		return false, nil
	}
}

// startBlock opens a toolUse block, the only kind Converse announces before
// its first delta.
func (c *converseStream) startBlock(start types.ContentBlockStartEvent) (corechat.PartDelta, error) {
	index, err := protocolBlockIndex(start.ContentBlockIndex)
	if err != nil {
		return corechat.PartDelta{}, err
	}
	if lo.IsNil(start.Start) {
		return corechat.PartDelta{}, fmt.Errorf("bedrock: stream: %w: missing content block start payload", corechat.ErrInvalidResponse)
	}
	tool, ok := start.Start.(*types.ContentBlockStartMemberToolUse)
	if !ok {
		return corechat.PartDelta{}, fmt.Errorf("bedrock: stream: %w: content block start %T", errors.ErrUnsupported, start.Start)
	}
	if tool.Value.Type != "" {
		return corechat.PartDelta{}, fmt.Errorf("bedrock: stream: %w: tool use type %q", errors.ErrUnsupported, tool.Value.Type)
	}
	if tool.Value.ToolUseId == nil || *tool.Value.ToolUseId == "" || tool.Value.Name == nil || *tool.Value.Name == "" {
		return corechat.PartDelta{}, fmt.Errorf("bedrock: stream: %w: toolUse content block opened without an id or name", corechat.ErrInvalidResponse)
	}
	if _, exists := c.blocks[index]; exists {
		return corechat.PartDelta{}, fmt.Errorf("bedrock: stream: %w: content block %d started twice", corechat.ErrInvalidResponse, index)
	}
	for _, other := range c.blocks {
		if other.kind == protocolToolBlock && other.toolID == *tool.Value.ToolUseId {
			return corechat.PartDelta{}, fmt.Errorf("bedrock: stream: %w: tool id %q reused at content block %d", corechat.ErrInvalidResponse, other.toolID, index)
		}
	}
	part := corechat.NewToolCallDelta(corechat.ToolCallDelta{ID: *tool.Value.ToolUseId, Name: *tool.Value.Name})
	part.Index = int(index)
	if err := part.Validate(); err != nil {
		return corechat.PartDelta{}, err
	}
	c.blocks[index] = protocolStreamBlock{kind: protocolToolBlock, open: true, toolID: *tool.Value.ToolUseId, toolName: *tool.Value.Name}
	return part, nil
}

func (c *converseStream) stopBlock(stop types.ContentBlockStopEvent) error {
	index, err := protocolBlockIndex(stop.ContentBlockIndex)
	if err != nil {
		return err
	}
	block, exists := c.blocks[index]
	if !exists || !block.open {
		return fmt.Errorf("bedrock: stream: %w: content block %d is not open", corechat.ErrInvalidResponse, index)
	}
	block.open = false
	c.blocks[index] = block
	return nil
}

func (c *converseStream) checkMessageStop(stop types.MessageStopEvent) error {
	if stop.StopReason == "" {
		return fmt.Errorf("bedrock: stream: %w: missing stop reason", corechat.ErrInvalidResponse)
	}
	for index, block := range c.blocks {
		if block.open {
			return fmt.Errorf("bedrock: stream: %w: content block %d is still open at messageStop", corechat.ErrInvalidResponse, index)
		}
	}
	return nil
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
		kind, part, err = mapReasoningDelta(value.Value)
		if err != nil {
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
	if err := c.admitDelta(index, kind); err != nil {
		return corechat.PartDelta{}, false, err
	}
	part.Index = int(index)
	include := part.Text != "" || len(part.ReasoningState) != 0 || part.ToolCall != nil && part.ToolCall.Arguments != ""
	if include {
		if err := part.Validate(); err != nil {
			return corechat.PartDelta{}, false, err
		}
	}
	return part, include, nil
}

// admitDelta requires the delta's block to be open with the same kind. Text
// and reasoning blocks have no native start event, so their first delta opens
// them even when its payload is empty; a tool block must have been started.
func (c *converseStream) admitDelta(index int32, kind protocolBlockKind) error {
	block, exists := c.blocks[index]
	if exists && (!block.open || block.kind != kind) || !exists && kind == protocolToolBlock {
		return fmt.Errorf("bedrock: stream: %w: delta for closed, mismatched, or unopened tool content block %d", corechat.ErrInvalidResponse, index)
	}
	if !exists {
		c.blocks[index] = protocolStreamBlock{kind: kind, open: true}
	}
	return nil
}

func mapReasoningDelta(delta types.ReasoningContentBlockDelta) (protocolBlockKind, corechat.PartDelta, error) {
	if lo.IsNil(delta) {
		return 0, corechat.PartDelta{}, fmt.Errorf("bedrock: stream: %w: missing reasoning delta payload", corechat.ErrInvalidResponse)
	}
	kind, reasoningKind := protocolReasoningTextBlock, chatReasoningText
	var part corechat.PartDelta
	switch reasoning := delta.(type) {
	case *types.ReasoningContentBlockDeltaMemberText:
		part = corechat.NewReasoningDelta(reasoning.Value, nil)
	case *types.ReasoningContentBlockDeltaMemberSignature:
		part = corechat.NewReasoningDelta("", []byte(reasoning.Value))
	case *types.ReasoningContentBlockDeltaMemberRedactedContent:
		kind, reasoningKind = protocolReasoningRedactedBlock, chatReasoningRedacted
		part = corechat.NewReasoningDelta("", reasoning.Value)
	default:
		return 0, corechat.PartDelta{}, fmt.Errorf("bedrock: stream: %w: reasoning delta %T", errors.ErrUnsupported, delta)
	}
	if err := setReasoningDeltaState(&part, reasoningKind); err != nil {
		return 0, corechat.PartDelta{}, err
	}
	return kind, part, nil
}
