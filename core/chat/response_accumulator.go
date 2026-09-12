package chat

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/Tangerg/scope/core/metadata"
)

// ResponseAccumulator is the only promotion path from transport deltas to a
// complete Response. Its zero value is ready to use. It must not be copied after
// first use and is not safe for concurrent use.
type ResponseAccumulator struct {
	metadata        *ResponseMetadata
	parts           []accumulatedPart
	messageMetadata metadata.Map
	outputMetadata  *OutputMetadata
	finishReason    FinishReason
	toolParts       map[string]int
	seen            bool
	hasMessage      bool
}

// accumulatedPart keeps growing text or tool arguments out of immutable strings.
// The complete Part is materialized only at the outward snapshot boundary.
type accumulatedPart struct {
	part    Part
	content []byte
}

func (a accumulatedPart) snapshot() Part {
	part := a.part.Clone()
	if part.Kind == PartToolCall {
		part.ToolCall.Arguments = string(a.content)
	} else {
		part.Text = string(a.content)
	}
	return part
}

// Add validates and applies one delta atomically; a failed merge leaves the
// accumulated stream unchanged. Atomicity does not imply concurrency safety.
func (r *ResponseAccumulator) Add(delta *ResponseDelta) error {
	if r == nil {
		return errors.New("chat: nil response accumulator")
	}
	if err := delta.Validate(); err != nil {
		return fmt.Errorf("chat: accumulate: %w", err)
	}
	if err := r.preflight(delta); err != nil {
		return fmt.Errorf("chat: accumulate: %w: %w", ErrInvalidResponse, err)
	}
	r.merge(delta)
	return nil
}

// Text returns the currently accumulated visible text without manufacturing a
// partial Response.
func (r *ResponseAccumulator) Text() string {
	if r == nil {
		return ""
	}
	var text strings.Builder
	for _, part := range r.parts {
		if part.part.Kind == PartText {
			text.Write(part.content)
		}
	}
	return text.String()
}

// Response promotes the accumulated stream only after a terminal finish reason
// has been observed and returns an independently owned snapshot.
func (r *ResponseAccumulator) Response() (*Response, error) {
	if r == nil || !r.seen {
		return nil, fmt.Errorf("%w: stream produced no deltas", ErrInvalidResponse)
	}
	if r.finishReason == "" {
		return nil, fmt.Errorf("%w: stream ended without a finish reason", ErrInvalidResponse)
	}
	output := &Output{FinishReason: r.finishReason}
	if r.hasMessage {
		message := &Message{Role: RoleAssistant, Metadata: r.messageMetadata.Clone(), Parts: make([]Part, len(r.parts))}
		for index := range r.parts {
			message.Parts[index] = r.parts[index].snapshot()
		}
		output.Message = message
	}
	if r.outputMetadata != nil {
		output.Metadata = r.outputMetadata.clone()
	}
	response := &Response{Output: output}
	if r.metadata != nil {
		response.Metadata = r.metadata.clone()
	}
	if err := response.Validate(); err != nil {
		return nil, fmt.Errorf("chat: accumulated response: %w", err)
	}
	return response, nil
}

// All cross-delta failures are checked before any owned state changes. The
// temporary identities cover tool calls introduced earlier in the same delta.
func (r *ResponseAccumulator) preflight(delta *ResponseDelta) error {
	if r.finishReason != "" {
		return errors.New("stream emitted a delta after its finish reason")
	}
	var last PartKind
	if len(r.parts) > 0 {
		last = r.parts[len(r.parts)-1].part.Kind
	}
	var introduced map[string]string
	for index, part := range delta.Parts {
		switch part.Kind {
		case PartDeltaCitation:
			if last != PartText {
				return fmt.Errorf("part %d: citation delta does not follow text", index)
			}
		case PartDeltaToolCall:
			call := part.ToolCall
			name, exists := introduced[call.ID]
			if position, found := r.toolParts[call.ID]; found {
				name, exists = r.parts[position].part.ToolCall.Name, true
			}
			if exists {
				if name != call.Name {
					return fmt.Errorf("part %d: tool call %q changed name from %q to %q", index, call.ID, name, call.Name)
				}
				continue
			}
			if introduced == nil {
				introduced = make(map[string]string)
			}
			introduced[call.ID] = call.Name
			last = PartToolCall
		case PartDeltaText:
			last = PartText
		case PartDeltaReasoning:
			last = PartReasoning
		case PartDeltaRefusal:
			last = PartRefusal
		case PartDeltaMedia:
			last = PartMedia
		}
	}
	return nil
}

func (r *ResponseAccumulator) merge(delta *ResponseDelta) {
	r.seen = true
	if delta.Metadata != nil {
		if r.metadata == nil {
			r.metadata = &ResponseMetadata{}
		}
		r.metadata.mergeValidated(*delta.Metadata)
	}
	if delta.OutputMetadata != nil {
		if r.outputMetadata == nil {
			r.outputMetadata = &OutputMetadata{}
		}
		mergeValidatedMetadata(&r.outputMetadata.Extra, delta.OutputMetadata.Extra)
	}
	r.finishReason = delta.FinishReason
	if len(delta.Parts) == 0 && len(delta.MessageMetadata) == 0 {
		return
	}
	r.hasMessage = true
	mergeValidatedMetadata(&r.messageMetadata, delta.MessageMetadata)
	for _, part := range delta.Parts {
		r.mergePart(part)
	}
}

func (r *ResponseAccumulator) mergePart(delta PartDelta) {
	switch delta.Kind {
	case PartDeltaText:
		r.mergeTextLike(PartText, delta)
	case PartDeltaRefusal:
		r.mergeTextLike(PartRefusal, delta)
	case PartDeltaReasoning:
		r.mergeTextLike(PartReasoning, delta)
	case PartDeltaMedia:
		part := NewMediaPart(delta.Media.Clone())
		part.Metadata = delta.Metadata.Clone()
		r.parts = append(r.parts, accumulatedPart{part: part})
	case PartDeltaToolCall:
		call := delta.ToolCall
		if position, exists := r.toolParts[call.ID]; exists {
			part := &r.parts[position]
			part.content = append(part.content, call.Arguments...)
			mergeValidatedMetadata(&part.part.Metadata, delta.Metadata)
			return
		}
		part := NewToolCallPart(ToolCall{ID: call.ID, Name: call.Name})
		part.Metadata = delta.Metadata.Clone()
		if r.toolParts == nil {
			r.toolParts = make(map[string]int)
		}
		r.toolParts[call.ID] = len(r.parts)
		r.parts = append(r.parts, accumulatedPart{part: part, content: []byte(call.Arguments)})
	case PartDeltaCitation:
		part := &r.parts[len(r.parts)-1].part
		part.Citations = append(part.Citations, *delta.Citation)
	}
}

func (r *ResponseAccumulator) mergeTextLike(kind PartKind, delta PartDelta) {
	if len(r.parts) == 0 || r.parts[len(r.parts)-1].part.Kind != kind || !r.parts[len(r.parts)-1].part.Metadata.Equal(delta.Metadata) {
		r.parts = append(r.parts, accumulatedPart{part: Part{Kind: kind, Metadata: delta.Metadata.Clone()}})
	}
	part := &r.parts[len(r.parts)-1]
	part.content = append(part.content, delta.Text...)
	part.part.ReasoningState = append(part.part.ReasoningState, delta.ReasoningState...)
}

// The accumulator owns target and validates source at Add's admission boundary.
// Only newly admitted bytes need copying or validation.
func mergeValidatedMetadata(target *metadata.Map, source metadata.Map) {
	if len(source) == 0 {
		return
	}
	if *target == nil {
		*target = make(metadata.Map, len(source))
	}
	for key, value := range source {
		(*target)[key] = bytes.Clone(value)
	}
}
