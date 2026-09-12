package ollama

import (
	"encoding/json"
	"errors"
	"fmt"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
)

const (
	// ResponseExtensionKey preserves the complete official Ollama response (or
	// the current official stream chunk), including log probabilities and image
	// output that Core does not normalize.
	ResponseExtensionKey        = "ollama/response"
	protocolNativeDoneReasonKey = "ollama/native_done_reason"
	protocolDurationsKey        = "ollama/durations_ns"
	protocolMetricsKey          = "ollama/metrics"
)

type protocolResponseMapper struct {
	hasToolCalls bool
	finished     bool
}

func newProtocolResponseMapper() *protocolResponseMapper {
	return new(protocolResponseMapper)
}

// terminated reports whether a chunk marked the generation done. mapResponse
// already refuses a nonterminal unary response; a stream has to ask the same
// question, because a body that ends between chunks is indistinguishable from
// a finished answer to whoever is reading the deltas.
func (p *protocolResponseMapper) terminated() bool { return p.finished }

func (p *protocolResponseMapper) mapResponse(requestModel string, response nativeChatResponse) (*corechat.Response, error) {
	if !response.Done {
		return nil, errors.New("ollama: non-streaming chat returned a nonterminal response")
	}
	metadata, err := response.metadata(requestModel)
	if err != nil {
		return nil, err
	}
	output, err := p.mapOutput(response)
	if err != nil {
		return nil, err
	}
	mapped := &corechat.Response{Output: output, Metadata: metadata}
	if err := mapped.Validate(); err != nil {
		return nil, fmt.Errorf("ollama: mapped response: %w", err)
	}
	return mapped, nil
}

func (p *protocolResponseMapper) mapDelta(requestModel string, response nativeChatResponse) (*corechat.ResponseDelta, error) {
	metadata, err := response.metadata(requestModel)
	if err != nil {
		return nil, err
	}
	mapped := &corechat.ResponseDelta{Metadata: metadata}
	parts, err := p.mapParts(response.Message)
	if err != nil {
		return nil, err
	}
	for index := range parts {
		part := parts[index]
		switch part.Kind {
		case corechat.PartText:
			mapped.Parts = append(mapped.Parts, corechat.NewTextDelta(part.Text))
		case corechat.PartMedia:
			mapped.Parts = append(mapped.Parts, corechat.NewMediaDelta(part.Media))
		case corechat.PartReasoning:
			mapped.Parts = append(mapped.Parts, corechat.NewReasoningDelta(part.Text, part.ReasoningState))
		case corechat.PartToolCall:
			mapped.Parts = append(mapped.Parts, corechat.NewToolCallDelta(corechat.ToolCallDelta{
				ID: part.ToolCall.ID, Name: part.ToolCall.Name, Arguments: part.ToolCall.Arguments,
			}))
		default:
			return nil, fmt.Errorf("ollama: unsupported stream part %q", part.Kind)
		}
	}
	if response.Done {
		if p.finished {
			return nil, errors.New("ollama: stream marked the generation done twice")
		}
		p.finished = true
		mapped.FinishReason = normalizeProtocolDoneReason(response.DoneReason, p.hasToolCalls)
		if response.DoneReason != "" {
			mapped.OutputMetadata = &corechat.OutputMetadata{}
			if err := mapped.OutputMetadata.Extra.Set(protocolNativeDoneReasonKey, response.DoneReason); err != nil {
				return nil, err
			}
		}
	}
	if err := mapped.Validate(); err != nil {
		return nil, fmt.Errorf("ollama: mapped response delta: %w", err)
	}
	return mapped, nil
}

func (p *protocolResponseMapper) mapOutput(response nativeChatResponse) (*corechat.Output, error) {
	output := &corechat.Output{}
	if response.DoneReason != "" {
		output.Metadata = &corechat.OutputMetadata{}
		if err := output.Metadata.Extra.Set(protocolNativeDoneReasonKey, response.DoneReason); err != nil {
			return nil, err
		}
	}
	parts, err := p.mapParts(response.Message)
	if err != nil {
		return nil, err
	}
	output.FinishReason = normalizeProtocolDoneReason(response.DoneReason, p.hasToolCalls)
	if len(parts) > 0 {
		output.Message = &corechat.Message{Role: corechat.RoleAssistant, Parts: parts}
	}
	return output, nil
}

func (p *protocolResponseMapper) mapParts(message nativeMessage) ([]corechat.Part, error) {
	var parts []corechat.Part
	if message.Thinking != "" {
		parts = append(parts, corechat.NewReasoningPart(message.Thinking, nil))
	}
	if message.Content != "" {
		parts = append(parts, corechat.NewTextPart(message.Content))
	}
	for index := range message.Images {
		image, err := media.NewBytes("image/*", message.Images[index])
		if err != nil {
			return nil, fmt.Errorf("ollama: message.images[%d]: %w", index, err)
		}
		parts = append(parts, corechat.NewMediaPart(image))
	}
	for i := range message.ToolCalls {
		toolCall := message.ToolCalls[i]
		if toolCall.Function.Name == "" {
			return nil, fmt.Errorf("ollama: message.tool_calls[%d]: empty function name", i)
		}
		arguments, err := json.Marshal(toolCall.Function.Arguments)
		if err != nil {
			return nil, fmt.Errorf("ollama: message.tool_calls[%d].arguments: %w", i, err)
		}
		id := toolCall.ID
		if id == "" {
			id = fmt.Sprintf("%s%d", protocolGeneratedToolPrefix, toolCall.Function.Index)
		}
		parts = append(parts, corechat.NewToolCallPart(corechat.ToolCall{
			ID:        id,
			Name:      toolCall.Function.Name,
			Arguments: string(arguments),
		}))
		p.hasToolCalls = true
	}
	return parts, nil
}

func normalizeProtocolDoneReason(reason string, hasToolCalls bool) corechat.FinishReason {
	switch reason {
	case "", "stop":
		if hasToolCalls {
			return corechat.FinishReasonToolCalls
		}
		return corechat.FinishReasonStop
	case "length":
		return corechat.FinishReasonLength
	default:
		return corechat.FinishReasonOther
	}
}

type protocolMetrics struct {
	PromptEvalCount int `json:"prompt_eval_count,omitempty"`
	EvalCount       int `json:"eval_count,omitempty"`
}
