package bedrock

import (
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	corechat "github.com/Tangerg/scope/core/chat"
)

type protocolToolIdentity struct {
	id   string
	name string
}

type protocolChunkAccumulator struct {
	model    string
	tools    map[int32]protocolToolIdentity
	finished bool
}

func newProtocolChunkAccumulator(model string) *protocolChunkAccumulator {
	return &protocolChunkAccumulator{model: model, tools: make(map[int32]protocolToolIdentity)}
}

// terminated reports whether a messageStop event arrived. stream.Err answers
// whether the event stream failed, which is a different question: an event
// stream can end cleanly in the middle of a message, and the deltas already
// yielded then describe a partial answer that nothing else marks as partial.
func (p *protocolChunkAccumulator) terminated() bool { return p.finished }

func (p *protocolChunkAccumulator) add(event types.ConverseStreamOutput) (*corechat.ResponseDelta, bool, error) {
	response := &corechat.ResponseDelta{Metadata: &corechat.ResponseMetadata{Model: p.model}}

	switch typed := event.(type) {
	case *types.ConverseStreamOutputMemberContentBlockStart:
		tool, ok := typed.Value.Start.(*types.ContentBlockStartMemberToolUse)
		if !ok || typed.Value.ContentBlockIndex == nil || tool.Value.ToolUseId == nil || tool.Value.Name == nil {
			return nil, false, nil
		}
		identity := protocolToolIdentity{id: *tool.Value.ToolUseId, name: *tool.Value.Name}
		p.tools[*typed.Value.ContentBlockIndex] = identity
		response.Parts = []corechat.PartDelta{corechat.NewToolCallDelta(corechat.ToolCallDelta{ID: identity.id, Name: identity.name})}
	case *types.ConverseStreamOutputMemberContentBlockDelta:
		part, include, err := p.mapDelta(typed.Value)
		if err != nil || !include {
			return nil, false, err
		}
		response.Parts = []corechat.PartDelta{part}
	case *types.ConverseStreamOutputMemberMessageStop:
		if p.finished {
			return nil, false, errors.New("bedrock: stream emitted more than one messageStop event")
		}
		p.finished = true
		response.FinishReason = mapProtocolStopReason(typed.Value.StopReason)
		if response.FinishReason == corechat.FinishReasonOther {
			response.OutputMetadata = &corechat.OutputMetadata{}
			if err := response.OutputMetadata.Extra.Set(chatNativeFinishReasonKey, string(typed.Value.StopReason)); err != nil {
				return nil, false, err
			}
		}
	case *types.ConverseStreamOutputMemberMetadata:
		if typed.Value.Usage == nil {
			return nil, false, nil
		}
		response.Metadata.Usage = mapProtocolUsage(typed.Value.Usage)
	default:
		return nil, false, nil
	}

	if err := response.Validate(); err != nil {
		return nil, false, fmt.Errorf("bedrock: stream response: %w", err)
	}
	return response, true, nil
}

func (p *protocolChunkAccumulator) mapDelta(delta types.ContentBlockDeltaEvent) (corechat.PartDelta, bool, error) {
	switch value := delta.Delta.(type) {
	case *types.ContentBlockDeltaMemberText:
		if value.Value == "" {
			return corechat.PartDelta{}, false, nil
		}
		return corechat.NewTextDelta(value.Value), true, nil
	case *types.ContentBlockDeltaMemberReasoningContent:
		switch reasoning := value.Value.(type) {
		case *types.ReasoningContentBlockDeltaMemberText:
			if reasoning.Value == "" {
				return corechat.PartDelta{}, false, nil
			}
			part := corechat.NewReasoningDelta(reasoning.Value, nil)
			if err := setReasoningDeltaKind(&part, chatReasoningText); err != nil {
				return corechat.PartDelta{}, false, err
			}
			return part, true, nil
		case *types.ReasoningContentBlockDeltaMemberSignature:
			if reasoning.Value == "" {
				return corechat.PartDelta{}, false, nil
			}
			part := corechat.NewReasoningDelta("", []byte(reasoning.Value))
			if err := setReasoningDeltaKind(&part, chatReasoningText); err != nil {
				return corechat.PartDelta{}, false, err
			}
			return part, true, nil
		case *types.ReasoningContentBlockDeltaMemberRedactedContent:
			if len(reasoning.Value) == 0 {
				return corechat.PartDelta{}, false, nil
			}
			part := corechat.NewReasoningDelta("", reasoning.Value)
			if err := setReasoningDeltaKind(&part, chatReasoningRedacted); err != nil {
				return corechat.PartDelta{}, false, err
			}
			return part, true, nil
		}
	case *types.ContentBlockDeltaMemberToolUse:
		if value.Value.Input == nil || *value.Value.Input == "" || delta.ContentBlockIndex == nil {
			return corechat.PartDelta{}, false, nil
		}
		identity, ok := p.tools[*delta.ContentBlockIndex]
		if !ok {
			return corechat.PartDelta{}, false, fmt.Errorf("bedrock: tool delta for unknown content block %d", *delta.ContentBlockIndex)
		}
		return corechat.NewToolCallDelta(corechat.ToolCallDelta{
			ID: identity.id, Name: identity.name, Arguments: *value.Value.Input,
		}), true, nil
	}
	return corechat.PartDelta{}, false, nil
}
