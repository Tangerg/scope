package mistral

import (
	"errors"
	"fmt"

	corechat "github.com/Tangerg/scope/core/chat"
)

type chatStreamTool struct {
	id               string
	name             string
	pendingArguments string
}

type chatStreamState struct {
	tools    map[int]chatStreamTool
	finished bool
}

func newChatStreamState() *chatStreamState {
	return &chatStreamState{tools: make(map[int]chatStreamTool)}
}

// terminated reports whether a chunk carried a finish reason. A stream that
// stops without one delivered a partial answer, and neither the closing
// [DONE] marker nor a clean end of body distinguishes that from a complete
// one, so the caller must be told rather than handed the fragment.
func (c *chatStreamState) terminated() bool { return c.finished }

func (c *chatStreamState) mapChunk(chunk chatCompletionChunk) (*corechat.ResponseDelta, error) {
	response := &corechat.ResponseDelta{
		Metadata: &corechat.ResponseMetadata{
			ID: chunk.ID, Model: chunk.Model, Usage: mapMistralUsage(chunk.Usage),
		},
	}
	if err := response.Metadata.Extra.Set(streamChunkExtensionKey, chunk); err != nil {
		return nil, err
	}
	if len(chunk.Choices) > expectedResponseChoices {
		return nil, fmt.Errorf("mistral: stream chunk has %d choices; Core supports one output", len(chunk.Choices))
	}
	if len(chunk.Choices) == expectedResponseChoices {
		wireChoice := chunk.Choices[0]
		if wireChoice.Index != firstChoiceIndex {
			return nil, fmt.Errorf("mistral: stream choice index is %d, want %d", wireChoice.Index, firstChoiceIndex)
		}
		parts, err := mapMistralContentDeltas(wireChoice.Delta.Content)
		if err != nil {
			return nil, fmt.Errorf("mistral: stream output content: %w", err)
		}
		toolParts, err := c.mapToolDeltas(wireChoice.Delta.ToolCalls)
		if err != nil {
			return nil, fmt.Errorf("mistral: stream output tool calls: %w", err)
		}
		parts = append(parts, toolParts...)
		response.Parts = parts
		response.FinishReason = normalizeMistralFinishReason(wireChoice.FinishReason)
		if response.FinishReason != "" {
			if c.finished {
				return nil, errors.New("mistral: stream emitted more than one finish reason")
			}
			c.finished = true
		}
		if response.FinishReason == corechat.FinishReasonOther {
			response.OutputMetadata = &corechat.OutputMetadata{}
			if err := response.OutputMetadata.Extra.Set(nativeFinishReasonKey, wireChoice.FinishReason); err != nil {
				return nil, err
			}
		}
	}
	if err := response.Validate(); err != nil {
		return nil, fmt.Errorf("mistral: mapped stream chunk: %w", err)
	}
	return response, nil
}

func (c *chatStreamState) mapToolDeltas(calls []chatToolCall) ([]corechat.PartDelta, error) {
	parts := make([]corechat.PartDelta, 0, len(calls))
	for position := range calls {
		call := calls[position]
		index := call.Index
		tool := c.tools[index]
		if call.ID != "" {
			tool.id = call.ID
		}
		if call.Function.Name != "" {
			tool.name = call.Function.Name
		}
		arguments, err := mistralToolArguments(call.Function.Arguments)
		if err != nil {
			return nil, fmt.Errorf("tool call %d arguments: %w", index, err)
		}
		tool.pendingArguments += arguments
		c.tools[index] = tool
		if tool.id == "" || tool.name == "" {
			continue
		}
		deltaArguments := tool.pendingArguments
		tool.pendingArguments = ""
		c.tools[index] = tool
		parts = append(parts, corechat.NewToolCallDelta(corechat.ToolCallDelta{
			ID: tool.id, Name: tool.name, Arguments: deltaArguments,
		}))
	}
	return parts, nil
}
