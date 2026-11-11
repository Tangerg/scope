package openai

import (
	"encoding/base64"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	openaisdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared/constant"

	corechat "github.com/Tangerg/scope/core/chat"
	corecontent "github.com/Tangerg/scope/core/content"
	"github.com/Tangerg/scope/core/media"
)

type openAIStreamTool struct {
	id               string
	name             string
	pendingArguments string
}

// chatStreamPart identifies one part of the single streamed message: each
// content field builds one part, while tool calls and reasoning blocks carry
// their own native identity.
type chatStreamPart struct {
	kind  corechat.PartKind
	block string
}

var (
	chatStreamText    = chatStreamPart{kind: corechat.PartText}
	chatStreamRefusal = chatStreamPart{kind: corechat.PartRefusal}
	chatStreamAudio   = chatStreamPart{kind: corechat.PartMedia}
)

type openAIStreamState struct {
	identity streamIdentity
	parts    streamPartIndexes[chatStreamPart]
	tools    map[int64]openAIStreamTool
	sequence uint64
	dialect  responseDialect
	chunkKey string
	// nativeFinishKey preserves the provider's own reason when it maps to Other.
	nativeFinishKey string
	refused         bool
	stopReason      string
	params          *openaisdk.ChatCompletionNewParams
	audioID         string
	audio           []byte
	transcript      strings.Builder
	hasText         bool
}

func newOpenAIStreamState(dialect chatDialect) *openAIStreamState {
	var response responseDialect
	if dialect.response != nil {
		response = dialect.response()
	}
	return &openAIStreamState{
		tools:           make(map[int64]openAIStreamTool),
		dialect:         response,
		chunkKey:        protocolStreamChunkExtensionKey(dialect.Provider),
		nativeFinishKey: dialect.Provider + nativeFinishReasonSuffix,
	}
}

func (o *openAIStreamState) mapChunk(chunk openaisdk.ChatCompletionChunk) (*corechat.ResponseDelta, error) {
	if err := validateResponseWire[chatChunkWire](chunk.RawJSON()); err != nil {
		return nil, fmt.Errorf("%w: openai: stream response: %w", corechat.ErrInvalidResponse, err)
	}
	o.sequence++
	if err := o.identity.bind(chunk.ID, chunk.Model, chunk.Created); err != nil {
		return nil, err
	}
	mapped := &corechat.ResponseDelta{Metadata: o.identity.metadata()}
	usage, err := mapUsage(chunk.Usage)
	if err != nil {
		return nil, err
	}
	mapped.Metadata.Usage = usage
	if len(chunk.Choices) > 1 {
		return nil, fmt.Errorf("openai: stream chunk has %d choices; Core supports one output", len(chunk.Choices))
	}
	if len(chunk.Choices) == 1 {
		parts, err := o.mapChunkOutput(chunk.Choices[0])
		if err != nil {
			return nil, fmt.Errorf("openai: stream output: %w", err)
		}
		mapped.Parts = parts
	}
	if err := mapped.Metadata.Extra.Set(o.chunkKey, exactProviderResponse(chunk.RawJSON(), chunk)); err != nil {
		return nil, err
	}
	if err := mapped.Validate(); err != nil {
		return nil, fmt.Errorf("openai: mapped stream response: %w", err)
	}
	if len(chunk.Choices) == 1 && chunk.Choices[0].FinishReason != "" {
		o.stopReason = chunk.Choices[0].FinishReason
	}
	return mapped, nil
}

func (o *openAIStreamState) finished() bool {
	return o.stopReason != ""
}

func (o *openAIStreamState) complete(delta *corechat.ResponseDelta) (*corechat.ResponseDelta, error) {
	if delta == nil || o.stopReason == "" {
		return nil, fmt.Errorf("openai: stream: %w: missing terminal response", corechat.ErrInvalidResponse)
	}
	if o.stopReason == string(constant.ValueOf[constant.ToolCalls]()) && len(o.tools) == 0 {
		return nil, fmt.Errorf("%w: openai: tool_calls finish reason without a tool call", corechat.ErrInvalidResponse)
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
	if o.audioID != "" || len(o.audio) != 0 {
		format := string(o.params.Audio.Format)
		if format == "" {
			if audio, ok := o.params.ExtraFields()["audio"].(map[string]any); ok {
				format, _ = audio["format"].(string)
			}
		}
		var value *media.Media
		var err error
		if len(o.audio) != 0 {
			value, err = media.NewBytes(audioMIME(format), o.audio)
		} else {
			value, err = media.NewReference(audioMIME(format), o.audioID)
		}
		if err != nil {
			return nil, err
		}
		value.ID = o.audioID
		if !o.hasText && o.transcript.Len() != 0 {
			delta.Parts = append(delta.Parts, o.delta(chatStreamText, corechat.NewTextDelta(o.transcript.String())))
		}
		delta.Parts = append(delta.Parts, o.delta(chatStreamAudio, corechat.NewMediaDelta(value)))
	}
	delta.FinishReason = normalizeFinishReason(o.stopReason)
	if delta.FinishReason == corechat.FinishReasonOther {
		if err := delta.Metadata.Extra.Set(o.nativeFinishKey, o.stopReason); err != nil {
			return nil, err
		}
	}
	if o.refused {
		delta.FinishReason = corechat.FinishReasonRefusal
	}
	if err := delta.Validate(); err != nil {
		return nil, fmt.Errorf("openai: terminal stream response: %w", err)
	}
	return delta, nil
}

func (o *openAIStreamState) mapChunkOutput(choice openaisdk.ChatCompletionChunkChoice) ([]corechat.PartDelta, error) {
	if present(choice.Delta.JSON.FunctionCall) || choice.FinishReason == string(constant.ValueOf[constant.FunctionCall]()) {
		return nil, fmt.Errorf("%w: openai: deprecated function_call cannot identify a Core tool proposal", errors.ErrUnsupported)
	}
	if index, present := responseIndex(choice.JSON.Index); !present || index != 0 {
		return nil, fmt.Errorf("%w: choice index must be the reported integer zero", corechat.ErrInvalidResponse)
	}
	if o.finished() {
		return nil, validateOpenAIStreamTail(choice)
	}
	parts, err := o.reasoningParts(choice.Delta)
	if err != nil {
		return nil, err
	}
	if choice.Delta.Content != "" {
		o.hasText = true
		parts = append(parts, o.delta(chatStreamText, corechat.NewTextDelta(choice.Delta.Content)))
	}
	citations, err := o.citationParts(choice.Delta)
	if err != nil {
		return nil, err
	}
	parts = append(parts, citations...)
	for i := range choice.Delta.ToolCalls {
		call, include, err := o.mapChunkTool(choice.Delta.ToolCalls[i])
		if err != nil {
			return nil, fmt.Errorf("tool_calls[%d]: %w", i, err)
		}
		if include {
			parts = append(parts, call)
		}
	}
	if choice.Delta.Refusal != "" {
		o.refused = true
		parts = append(parts, o.delta(chatStreamRefusal, corechat.NewRefusalDelta(choice.Delta.Refusal)))
	}
	if present(choice.Delta.JSON.Audio) {
		if err := o.accumulateAudio(choice.Delta.Audio); err != nil {
			return nil, err
		}
	}
	return parts, nil
}

func (o *openAIStreamState) reasoningParts(delta openaisdk.ChatCompletionChunkChoiceDelta) ([]corechat.PartDelta, error) {
	if o.dialect == nil {
		return nil, nil
	}
	reasoning, err := o.dialect.ReasoningDeltas(delta, o.sequence)
	if err != nil {
		return nil, fmt.Errorf("response dialect: %w", err)
	}
	parts := make([]corechat.PartDelta, 0, len(reasoning))
	for _, block := range reasoning {
		part := corechat.NewReasoningDelta(block.part.Text, block.part.ReasoningState)
		part.Metadata = block.part.Metadata
		parts = append(parts, o.delta(chatStreamPart{kind: corechat.PartReasoning, block: block.block}, part))
	}
	return parts, nil
}

// citationParts reads the URL annotations the SDK leaves as an extra field.
func (o *openAIStreamState) citationParts(delta openaisdk.ChatCompletionChunkChoiceDelta) ([]corechat.PartDelta, error) {
	field, found := delta.JSON.ExtraFields[chunkAnnotationsField]
	if !found || !present(field) {
		return nil, nil
	}
	var annotations []openaisdk.ChatCompletionMessageAnnotation
	if err := jsonv2.Unmarshal([]byte(field.Raw()), &annotations); err != nil {
		return nil, fmt.Errorf("annotations: %w", err)
	}
	parts := make([]corechat.PartDelta, 0, len(annotations))
	for _, annotation := range annotations {
		citation := corecontent.Citation{Source: corecontent.CitationSource{Kind: corecontent.CitationSourceURI, Value: annotation.URLCitation.URL}, Title: annotation.URLCitation.Title}
		parts = append(parts, o.delta(chatStreamText, corechat.NewCitationDelta(citation)))
	}
	return parts, nil
}

// chunkAnnotationsField is the delta member the SDK does not model.
const chunkAnnotationsField = "annotations"

// accumulateAudio appends one audio fragment; a stream carries one audio
// identity, so a later fragment cannot name another.
func (o *openAIStreamState) accumulateAudio(audio openaisdk.ChatCompletionChunkChoiceDeltaAudio) error {
	if audio.ID != "" {
		if o.audioID != "" && o.audioID != audio.ID {
			return fmt.Errorf("audio identity changed from %q to %q", o.audioID, audio.ID)
		}
		o.audioID = audio.ID
	}
	data, err := base64.StdEncoding.DecodeString(audio.Data)
	if err != nil {
		return fmt.Errorf("audio data: %w", err)
	}
	o.audio = append(o.audio, data...)
	o.transcript.WriteString(audio.Transcript)
	return nil
}

// Audio expiry is a native metadata tail with a choice, unlike usage. It must
// remain observable without reopening generation after the stop reason.
func validateOpenAIStreamTail(choice openaisdk.ChatCompletionChunkChoice) error {
	if choice.FinishReason != "" {
		return fmt.Errorf("openai: stream: %w: more than one finish reason", corechat.ErrInvalidResponse)
	}
	var fields map[string]json.RawMessage
	if err := jsonv2.Unmarshal([]byte(choice.Delta.RawJSON()), &fields); err != nil {
		return fmt.Errorf("openai: stream: %w: invalid terminal delta: %w", corechat.ErrInvalidResponse, err)
	}
	if len(fields) == 0 {
		return nil
	}
	if len(fields) == 1 && fields["audio"] != nil {
		var expiry struct {
			ExpiresAt *int64 `json:"expires_at"`
		}
		if err := jsonv2.Unmarshal(fields["audio"], &expiry, jsonv2.RejectUnknownMembers(true)); err == nil && expiry.ExpiresAt != nil {
			return nil
		}
	}
	return fmt.Errorf("openai: stream: %w: generation continued after finish reason", corechat.ErrInvalidResponse)
}

func (o *openAIStreamState) delta(part chatStreamPart, delta corechat.PartDelta) corechat.PartDelta {
	delta.Index = o.parts.of(part)
	return delta
}

func (o *openAIStreamState) mapChunkTool(delta openaisdk.ChatCompletionChunkChoiceDeltaToolCall) (corechat.PartDelta, bool, error) {
	// Omitting the index cannot identify a fragment as belonging to call zero;
	// the SDK represents missing and explicit zero with the same Go value.
	index, indexed := responseIndex(delta.JSON.Index)
	if !indexed {
		return corechat.PartDelta{}, false, fmt.Errorf("%w: tool fragment requires a nonnegative index", corechat.ErrInvalidResponse)
	}
	if delta.Type != "" && delta.Type != string(constant.ValueOf[constant.Function]()) {
		return corechat.PartDelta{}, false, fmt.Errorf("unsupported type %q", delta.Type)
	}
	state := o.tools[index]
	if delta.ID != "" {
		if state.id != "" && state.id != delta.ID {
			return corechat.PartDelta{}, false, fmt.Errorf("%w: tool index %d changed id from %q to %q", corechat.ErrInvalidResponse, index, state.id, delta.ID)
		}
		for otherIndex, tool := range o.tools {
			if otherIndex != index && tool.id == delta.ID {
				return corechat.PartDelta{}, false, fmt.Errorf("%w: tool id %q reused at index %d", corechat.ErrInvalidResponse, delta.ID, index)
			}
		}
		state.id = delta.ID
	}
	if delta.Function.Name != "" {
		if state.name != "" && state.name != delta.Function.Name {
			return corechat.PartDelta{}, false, fmt.Errorf("%w: tool index %d changed name from %q to %q", corechat.ErrInvalidResponse, index, state.name, delta.Function.Name)
		}
		state.name = delta.Function.Name
	}
	state.pendingArguments += delta.Function.Arguments
	o.tools[index] = state
	if state.id == "" || state.name == "" {
		return corechat.PartDelta{}, false, nil
	}
	arguments := state.pendingArguments
	state.pendingArguments = ""
	o.tools[index] = state
	part := corechat.NewToolCallDelta(corechat.ToolCallDelta{ID: state.id, Name: state.name, Arguments: arguments})
	return o.delta(chatStreamPart{kind: corechat.PartToolCall, block: strconv.FormatInt(index, 10)}, part), true, nil
}
