package mistral

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
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
	responseID    string
	model         string
	parts         chatStreamParts
	tools         map[int]chatStreamTool
	finish        finishReason
	thinkingBlock uint64
}

func newChatStreamState() *chatStreamState {
	return &chatStreamState{tools: make(map[int]chatStreamTool), parts: chatStreamParts{tools: make(map[int]int)}}
}

// chatStreamParts assigns Core part identity to Mistral chunks, which carry
// none of their own. Text continues across chunks until another part opens;
// each thinking child keeps its own replay frame, media is complete in one
// chunk, and a tool call keeps its native index.
type chatStreamParts struct {
	count    int
	openText *int
	lastText *int
	tools    map[int]int
}

func (c *chatStreamParts) open() int {
	c.openText = nil
	index := c.count
	c.count++
	return index
}

func (c *chatStreamParts) text() int {
	if c.openText == nil {
		index := c.open()
		c.openText = &index
	}
	c.lastText = c.openText
	return *c.openText
}

func (c *chatStreamParts) tool(native int) int {
	index, found := c.tools[native]
	if !found {
		index = c.open()
		c.tools[native] = index
	}
	return index
}

func (c *chatStreamParts) citation() (int, error) {
	if c.lastText == nil {
		return 0, fmt.Errorf("%w: reference precedes any text", corechat.ErrInvalidResponse)
	}
	return *c.lastText, nil
}

// terminated reports whether a chunk carried a finish reason. A stream that
// stops without one delivered a partial answer, and neither the closing
// [DONE] marker nor a clean end of body distinguishes that from a complete
// one, so the caller must be told rather than handed the fragment.
func (c *chatStreamState) terminated() bool { return c.finish != "" }

func (c *chatStreamState) mapChunk(chunk chatCompletionChunk) (*corechat.ResponseDelta, error) {
	if err := c.bindIdentity(chunk); err != nil {
		return nil, err
	}
	response := &corechat.ResponseDelta{
		Metadata: &corechat.ResponseMetadata{
			ID: c.responseID, Model: c.model, Usage: chunk.Usage.usage(),
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
			return nil, fmt.Errorf("mistral: %w: stream choice index is %d, want %d", corechat.ErrInvalidResponse, wireChoice.Index, firstChoiceIndex)
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

func (c *chatStreamState) bindIdentity(chunk chatCompletionChunk) error {
	if chunk.ID != "" && c.responseID != "" && chunk.ID != c.responseID {
		return fmt.Errorf("mistral: stream: %w: response id changed from %q to %q", corechat.ErrInvalidResponse, c.responseID, chunk.ID)
	}
	if chunk.Model != "" && c.model != "" && chunk.Model != c.model {
		return fmt.Errorf("mistral: stream: %w: model changed from %q to %q", corechat.ErrInvalidResponse, c.model, chunk.Model)
	}
	if chunk.ID != "" {
		c.responseID = chunk.ID
	}
	if chunk.Model != "" {
		c.model = chunk.Model
	}
	return nil
}

func (c *chatStreamState) complete(delta *corechat.ResponseDelta) (*corechat.ResponseDelta, error) {
	if delta == nil || !c.terminated() {
		return nil, fmt.Errorf("mistral: stream: %w: missing terminal response", corechat.ErrInvalidResponse)
	}
	if err := c.requireIdentifiedTools(); err != nil {
		return nil, err
	}
	delta.FinishReason = c.finish.normalized()
	if err := c.finish.preserveNative(delta.Metadata); err != nil {
		return nil, err
	}
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
		if err := call.validate(); err != nil {
			return nil, err
		}
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
		part := corechat.NewToolCallDelta(corechat.ToolCallDelta{ID: tool.id, Name: tool.name, Arguments: deltaArguments})
		part.Index = c.parts.tool(index)
		parts = append(parts, part)
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
		part := corechat.NewTextDelta(text)
		part.Index = c.parts.text()
		return []corechat.PartDelta{part}, nil
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
				part := corechat.NewCitationDelta(citations[citationIndex])
				if part.Index, err = c.parts.citation(); err != nil {
					return nil, fmt.Errorf("chunk[%d]: %w", index, err)
				}
				deltas = append(deltas, part)
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
			delta := corechat.NewTextDelta(part.Text)
			delta.Index = c.parts.text()
			deltas = append(deltas, delta)
		case corechat.PartMedia:
			delta := corechat.NewMediaDelta(part.Media)
			delta.Index = c.parts.open()
			deltas = append(deltas, delta)
		default:
			return nil, fmt.Errorf("chunk[%d]: unsupported stream part %q", index, part.Kind)
		}
	}
	return deltas, nil
}

func (c *chatStreamState) mapThinkingDeltas(raw json.RawMessage) ([]corechat.PartDelta, error) {
	var chunk thinkingWireChunk
	if err := jsonv2.Unmarshal(raw, &chunk); err != nil {
		return nil, err
	}
	if c.thinkingBlock == 0 {
		c.thinkingBlock = 1
	}
	parts, err := chunk.deltas(c.thinkingBlock)
	if err != nil {
		return nil, err
	}
	for index := range parts {
		parts[index].Index = c.parts.open()
	}
	if chunk.Closed == thinkingClosureClosed {
		c.thinkingBlock++
	}
	return parts, nil
}
