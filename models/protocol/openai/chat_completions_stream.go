package openai

import (
	"fmt"
	"maps"
	"slices"
	"time"

	openaisdk "github.com/openai/openai-go/v3"

	corechat "github.com/Tangerg/scope/core/chat"
)

type openAIStreamTool struct {
	id               string
	name             string
	pendingArguments string
}

type openAIStreamState struct {
	tools    map[int64]openAIStreamTool
	dialect  responseDialect
	chunkKey string
	refused  bool
	finish   corechat.FinishReason
}

func newOpenAIStreamState(dialect Dialect) *openAIStreamState {
	return &openAIStreamState{
		tools:    make(map[int64]openAIStreamTool),
		dialect:  dialect.response,
		chunkKey: protocolStreamChunkExtensionKey(dialect.Provider),
	}
}

func (o *openAIStreamState) mapChunk(chunk openaisdk.ChatCompletionChunk) (*corechat.ResponseDelta, error) {
	mapped := &corechat.ResponseDelta{
		Metadata: &corechat.ResponseMetadata{
			ID:    chunk.ID,
			Model: chunk.Model,
			Usage: mapUsage(chunk.Usage),
		},
	}
	if chunk.Created > 0 {
		mapped.Metadata.CreatedAt = time.Unix(chunk.Created, 0).UTC()
	}
	if len(chunk.Choices) > 1 {
		return nil, fmt.Errorf("openai: stream chunk has %d choices; Core supports one output", len(chunk.Choices))
	}
	if len(chunk.Choices) == 1 {
		parts, finish, err := o.mapChunkOutput(chunk.Choices[0])
		if err != nil {
			return nil, fmt.Errorf("openai: stream output: %w", err)
		}
		mapped.Parts = parts
		if finish != "" {
			if o.finish != "" {
				return nil, fmt.Errorf("openai: stream emitted more than one finish reason")
			}
			o.finish = finish
		}
	}
	if err := mapped.Metadata.Extra.Set(o.chunkKey, exactProviderResponse(chunk.RawJSON(), chunk)); err != nil {
		return nil, err
	}
	if err := mapped.Validate(); err != nil {
		return nil, fmt.Errorf("openai: mapped stream response: %w", err)
	}
	return mapped, nil
}

func (o *openAIStreamState) finished() bool {
	return o.finish != ""
}

func (o *openAIStreamState) complete(delta *corechat.ResponseDelta) (*corechat.ResponseDelta, error) {
	if delta == nil || o.finish == "" {
		return nil, fmt.Errorf("openai: stream: %w: missing terminal response", corechat.ErrInvalidResponse)
	}
	// A Core tool-call delta cannot carry arguments without an id and a name,
	// so an index is held back until both arrive -- OpenAI sends them on the
	// first delta at that index. An index still held at the end is a call the
	// stream described and never identified, and letting the terminal response
	// through would hand back something that looks whole while missing a tool
	// call, with its buffered arguments discarded.
	for _, index := range slices.Sorted(maps.Keys(o.tools)) {
		tool := o.tools[index]
		if tool.id != "" && tool.name != "" {
			continue
		}
		return nil, fmt.Errorf("openai: stream: %w: tool_calls[%d] ended with id=%q name=%q and %d buffered argument byte(s), so the call cannot be reported",
			corechat.ErrInvalidResponse, index, tool.id, tool.name, len(tool.pendingArguments))
	}
	delta.FinishReason = o.finish
	if err := delta.Validate(); err != nil {
		return nil, fmt.Errorf("openai: terminal stream response: %w", err)
	}
	return delta, nil
}

func (o *openAIStreamState) mapChunkOutput(choice openaisdk.ChatCompletionChunkChoice) ([]corechat.PartDelta, corechat.FinishReason, error) {
	if choice.Index != 0 {
		return nil, "", fmt.Errorf("choice index is %d, want 0", choice.Index)
	}
	message := &corechat.Message{Role: corechat.RoleAssistant}
	if choice.Delta.Content != "" {
		message.Parts = append(message.Parts, corechat.NewTextPart(choice.Delta.Content))
	}
	if o.dialect != nil {
		if err := o.dialect.FinalizeDelta(choice.Delta, message); err != nil {
			return nil, "", fmt.Errorf("response dialect: %w", err)
		}
	}
	parts, err := stablePartsAsDeltas(message.Parts)
	if err != nil {
		return nil, "", err
	}
	for i := range choice.Delta.ToolCalls {
		call, include, err := o.mapChunkTool(choice.Delta.ToolCalls[i])
		if err != nil {
			return nil, "", fmt.Errorf("tool_calls[%d]: %w", i, err)
		}
		if include {
			parts = append(parts, corechat.NewToolCallDelta(call))
		}
	}
	if choice.Delta.Refusal != "" {
		o.refused = true
		parts = append(parts, corechat.NewRefusalDelta(choice.Delta.Refusal))
	}
	finish := normalizeFinishReason(choice.FinishReason)
	if finish != "" && o.refused {
		finish = corechat.FinishReasonRefusal
	}
	return parts, finish, nil
}

func stablePartsAsDeltas(parts []corechat.Part) ([]corechat.PartDelta, error) {
	deltas := make([]corechat.PartDelta, 0, len(parts))
	for index := range parts {
		switch parts[index].Kind {
		case corechat.PartText:
			delta := corechat.NewTextDelta(parts[index].Text)
			delta.Metadata = parts[index].Metadata.Clone()
			deltas = append(deltas, delta)
		case corechat.PartReasoning:
			delta := corechat.NewReasoningDelta(parts[index].Text, parts[index].ReasoningState)
			delta.Metadata = parts[index].Metadata.Clone()
			deltas = append(deltas, delta)
		default:
			return nil, fmt.Errorf("stream dialect produced unsupported part %q", parts[index].Kind)
		}
	}
	return deltas, nil
}

func (o *openAIStreamState) mapChunkTool(delta openaisdk.ChatCompletionChunkChoiceDeltaToolCall) (corechat.ToolCallDelta, bool, error) {
	if delta.Type != "" && delta.Type != "function" {
		return corechat.ToolCallDelta{}, false, fmt.Errorf("unsupported type %q", delta.Type)
	}
	state := o.tools[delta.Index]
	if delta.ID != "" {
		state.id = delta.ID
	}
	if delta.Function.Name != "" {
		state.name = delta.Function.Name
	}
	state.pendingArguments += delta.Function.Arguments
	o.tools[delta.Index] = state
	if state.id == "" || state.name == "" {
		return corechat.ToolCallDelta{}, false, nil
	}
	arguments := state.pendingArguments
	state.pendingArguments = ""
	o.tools[delta.Index] = state
	return corechat.ToolCallDelta{
		ID:        state.id,
		Name:      state.name,
		Arguments: arguments,
	}, true, nil
}
