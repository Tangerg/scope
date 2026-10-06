package mistral

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"slices"

	corechat "github.com/Tangerg/scope/core/chat"
)

type chatStreamTool struct {
	id               string
	name             string
	pendingArguments string
}

type chatStreamState struct {
	tools            map[int]chatStreamTool
	finish           finishReason
	thinkingBlock    uint64
	thinkingPosition uint64
}

func newChatStreamState() *chatStreamState {
	return &chatStreamState{tools: make(map[int]chatStreamTool)}
}

// terminated reports whether a chunk carried a finish reason. A stream that
// stops without one delivered a partial answer, and neither the closing
// [DONE] marker nor a clean end of body distinguishes that from a complete
// one, so the caller must be told rather than handed the fragment.
func (c *chatStreamState) terminated() bool { return c.finish != "" }

func (c *chatStreamState) mapChunk(chunk chatCompletionChunk) (*corechat.ResponseDelta, error) {
	response := &corechat.ResponseDelta{
		Metadata: &corechat.ResponseMetadata{
			ID: chunk.ID, Model: chunk.Model, Usage: chunk.Usage.usage(),
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
		if c.terminated() && len(wireChoice.Delta.ToolCalls) != 0 {
			return nil, fmt.Errorf("mistral: stream: %w: tool calls after finish_reason", corechat.ErrInvalidResponse)
		}
		parts, err := c.mapContentDeltas(wireChoice.Delta.Content)
		if err != nil {
			return nil, fmt.Errorf("mistral: stream output content: %w", err)
		}
		if c.terminated() && len(parts) != 0 {
			return nil, fmt.Errorf("mistral: stream: %w: content after finish_reason", corechat.ErrInvalidResponse)
		}
		toolParts, err := c.mapToolDeltas(wireChoice.Delta.ToolCalls)
		if err != nil {
			return nil, fmt.Errorf("mistral: stream output tool calls: %w", err)
		}
		parts = append(parts, toolParts...)
		response.Parts = parts
		if wireChoice.FinishReason != "" {
			if c.terminated() {
				return nil, fmt.Errorf("mistral: stream: %w: more than one finish_reason", corechat.ErrInvalidResponse)
			}
			c.finish = wireChoice.FinishReason
		}
	}
	if err := response.Validate(); err != nil {
		return nil, fmt.Errorf("mistral: mapped stream chunk: %w", err)
	}
	return response, nil
}

func (c *chatStreamState) complete(delta *corechat.ResponseDelta) (*corechat.ResponseDelta, error) {
	if delta == nil || !c.terminated() {
		return nil, fmt.Errorf("mistral: stream: %w: missing terminal response", corechat.ErrInvalidResponse)
	}
	if err := c.requireIdentifiedTools(); err != nil {
		return nil, err
	}
	delta.FinishReason = c.finish.normalized()
	outputMetadata, err := c.finish.metadata()
	if err != nil {
		return nil, err
	}
	delta.OutputMetadata = outputMetadata
	if err := delta.Validate(); err != nil {
		return nil, fmt.Errorf("mistral: terminal stream response: %w", err)
	}
	return delta, nil
}

// An unidentified buffer is a call the stream described but cannot report.
// Publishing completion would hide that call and discard its arguments.
func (c *chatStreamState) requireIdentifiedTools() error {
	for _, index := range slices.Sorted(maps.Keys(c.tools)) {
		tool := c.tools[index]
		if tool.id != "" && tool.name != "" {
			continue
		}
		return fmt.Errorf("mistral: stream: %w: tool call %d ended with id=%q name=%q and %d buffered argument byte(s), so the call cannot be reported",
			corechat.ErrInvalidResponse, index, tool.id, tool.name, len(tool.pendingArguments))
	}
	return nil
}

func (c *chatStreamState) mapToolDeltas(calls []chatToolCall) ([]corechat.PartDelta, error) {
	parts := make([]corechat.PartDelta, 0, len(calls))
	for position := range calls {
		call := calls[position]
		index := call.Index
		tool := c.tools[index]
		if call.ID != "" {
			if tool.id != "" && tool.id != call.ID {
				return nil, fmt.Errorf("%w: tool call %d changed id from %q to %q", corechat.ErrInvalidResponse, index, tool.id, call.ID)
			}
			for otherIndex, other := range c.tools {
				if otherIndex != index && other.id == call.ID {
					return nil, fmt.Errorf("%w: tool id %q reused at index %d", corechat.ErrInvalidResponse, call.ID, index)
				}
			}
			tool.id = call.ID
		}
		if call.Function.Name != "" {
			if tool.name != "" && tool.name != call.Function.Name {
				return nil, fmt.Errorf("%w: tool call %d changed name from %q to %q", corechat.ErrInvalidResponse, index, tool.name, call.Function.Name)
			}
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

func (c *chatStreamState) mapContentDeltas(raw json.RawMessage) ([]corechat.PartDelta, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	if trimmed[0] == '"' {
		var text string
		if err := jsonv2.Unmarshal(trimmed, &text); err != nil {
			return nil, err
		}
		if text == "" {
			return nil, nil
		}
		return []corechat.PartDelta{corechat.NewTextDelta(text)}, nil
	}
	var chunks []json.RawMessage
	if err := jsonv2.Unmarshal(trimmed, &chunks); err != nil {
		return nil, err
	}
	deltas := make([]corechat.PartDelta, 0, len(chunks))
	for index := range chunks {
		citations, reference, err := mapMistralReferenceChunk(chunks[index])
		if err != nil {
			return nil, fmt.Errorf("chunk[%d]: %w", index, err)
		}
		if reference {
			for citationIndex := range citations {
				deltas = append(deltas, corechat.NewCitationDelta(citations[citationIndex]))
			}
			continue
		}
		var discriminator struct {
			Type contentType `json:"type"`
		}
		if err = jsonv2.Unmarshal(chunks[index], &discriminator); err != nil {
			return nil, fmt.Errorf("chunk[%d]: %w", index, err)
		}
		if discriminator.Type == contentTypeThinking {
			parts, thinkingErr := c.mapThinkingDeltas(chunks[index])
			if thinkingErr != nil {
				return nil, fmt.Errorf("chunk[%d]: %w", index, thinkingErr)
			}
			deltas = append(deltas, parts...)
			continue
		}
		part, include, err := mapMistralContentChunk(chunks[index])
		if err != nil {
			return nil, fmt.Errorf("chunk[%d]: %w", index, err)
		}
		if !include {
			continue
		}
		switch part.Kind {
		case corechat.PartText:
			deltas = append(deltas, corechat.NewTextDelta(part.Text))
		case corechat.PartMedia:
			deltas = append(deltas, corechat.NewMediaDelta(part.Media))
		default:
			return nil, fmt.Errorf("chunk[%d]: unsupported stream part %q", index, part.Kind)
		}
	}
	return deltas, nil
}

func (c *chatStreamState) mapThinkingDeltas(raw json.RawMessage) ([]corechat.PartDelta, error) {
	var fields map[string]json.RawMessage
	if err := jsonv2.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	var children []map[string]json.RawMessage
	if len(fields["thinking"]) == 0 || bytes.Equal(bytes.TrimSpace(fields["thinking"]), []byte("null")) {
		return nil, errors.New("thinking content must be an array")
	}
	if err := jsonv2.Unmarshal(fields["thinking"], &children); err != nil {
		return nil, err
	}
	delete(fields, "type")
	delete(fields, "thinking")
	if c.thinkingBlock == 0 {
		c.thinkingBlock = 1
	}
	parts := make([]corechat.PartDelta, 0, max(1, len(children)))
	for index := range max(1, len(children)) {
		state := thinkingPartState{Block: c.thinkingBlock}
		text := ""
		if len(children) > 0 {
			state.Content = children[index]
			kind, err := state.contentType()
			if err != nil {
				return nil, fmt.Errorf("thinking[%d]: %w", index, err)
			}
			if kind == contentTypeText {
				var value *string
				if err := jsonv2.Unmarshal(state.Content["text"], &value); err != nil {
					return nil, fmt.Errorf("thinking[%d].text: %w", index, err)
				}
				if value == nil {
					return nil, fmt.Errorf("thinking[%d].text is required", index)
				}
				text = *value
				delete(state.Content, "text")
			}
		}
		if index == max(1, len(children))-1 {
			state.Fields = fields
		}
		frame, err := encodeThinkingFrame(state)
		if err != nil {
			return nil, err
		}
		part := corechat.NewReasoningDelta(text, frame)
		identity := struct {
			Block    uint64 `json:"block"`
			Position uint64 `json:"position"`
		}{Block: c.thinkingBlock, Position: c.thinkingPosition}
		if err := part.Metadata.Set(thinkingPartIdentityKey, identity); err != nil {
			return nil, err
		}
		parts = append(parts, part)
		c.thinkingPosition++
		if state.closed() {
			c.thinkingBlock++
		}
	}
	return parts, nil
}
