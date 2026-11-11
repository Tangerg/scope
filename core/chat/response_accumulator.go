package chat

import (
	"bytes"
	"errors"
	"fmt"
	"iter"
	"strings"

	"github.com/Tangerg/scope/core/metadata"
)

// ResponseAccumulator is the only promotion path from transport deltas to a
// complete Response. Its zero value is ready to use. It must not be copied after
// first use and is not safe for concurrent use.
type ResponseAccumulator struct {
	metadata        *ResponseMetadata
	parts           []accumulatedPart
	positions       map[int]int
	messageMetadata metadata.Map
	finishReason    FinishReason
	seen            bool
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
// accumulated stream unchanged.
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

// Collect drains one stream to its terminal response. A downstream failure after
// completion retains that generation and its usage without reporting success.
func Collect(sequence iter.Seq2[*ResponseDelta, error]) (*Response, error) {
	if sequence == nil {
		return nil, ErrNilStream
	}
	var accumulator ResponseAccumulator
	for delta, err := range sequence {
		if err != nil {
			if accumulator.finishReason != "" {
				response, admission := accumulator.Response()
				if admission != nil {
					return nil, errors.Join(err, admission)
				}
				return response, err
			}
			return nil, err
		}
		if err := accumulator.Add(delta); err != nil {
			return nil, err
		}
	}
	return accumulator.Response()
}

// HasDeltas reports admitted stream evidence without promoting an incomplete
// generation to a Response. Failed Add calls never establish this evidence.
func (r *ResponseAccumulator) HasDeltas() bool { return r != nil && r.seen }

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
	if len(r.parts) > 0 || len(r.messageMetadata) > 0 {
		message := &Message{Role: RoleAssistant, Metadata: r.messageMetadata.Clone(), Parts: make([]Part, len(r.parts))}
		for index := range r.parts {
			message.Parts[index] = r.parts[index].snapshot()
		}
		output.Message = message
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
// temporary openings cover parts introduced earlier in the same delta.
func (r *ResponseAccumulator) preflight(delta *ResponseDelta) error {
	if r.finishReason != "" {
		return errors.New("stream emitted a delta after its finish reason")
	}
	var opened map[int]partOpening
	for position, part := range delta.Parts {
		opening, exists := r.opening(part.Index)
		if !exists {
			opening, exists = opened[part.Index]
		}
		if !exists {
			if opened == nil {
				opened = make(map[int]partOpening)
			}
			opened[part.Index] = newPartOpening(part)
			continue
		}
		if err := opening.admit(part); err != nil {
			return fmt.Errorf("part %d at index %d: %w", position, part.Index, err)
		}
	}
	return nil
}

func (r *ResponseAccumulator) opening(index int) (partOpening, bool) {
	position, exists := r.positions[index]
	if !exists {
		return partOpening{}, false
	}
	part := r.parts[position].part
	opening := partOpening{kind: part.Kind}
	if part.ToolCall != nil {
		opening.toolID, opening.toolName = part.ToolCall.ID, part.ToolCall.Name
	}
	return opening, true
}

// partOpening is what every later delta of one part must agree with.
type partOpening struct {
	kind     PartKind
	toolID   string
	toolName string
}

func newPartOpening(delta PartDelta) partOpening {
	opening := partOpening{kind: delta.Kind.partKind()}
	if delta.ToolCall != nil {
		opening.toolID, opening.toolName = delta.ToolCall.ID, delta.ToolCall.Name
	}
	return opening
}

func (p partOpening) admit(delta PartDelta) error {
	switch {
	case delta.Kind.partKind() != p.kind:
		return fmt.Errorf("%s delta cannot extend a %s part", delta.Kind, p.kind)
	case delta.Kind == PartDeltaMedia:
		return errors.New("media delta cannot extend a complete media part")
	case delta.Kind == PartDeltaToolCall && (delta.ToolCall.ID != p.toolID || delta.ToolCall.Name != p.toolName):
		return fmt.Errorf("tool call %q (%q) cannot extend tool call %q (%q)", delta.ToolCall.ID, delta.ToolCall.Name, p.toolID, p.toolName)
	default:
		return nil
	}
}

func (r *ResponseAccumulator) merge(delta *ResponseDelta) {
	r.seen = true
	if delta.Metadata != nil {
		if r.metadata == nil {
			r.metadata = &ResponseMetadata{}
		}
		r.metadata.mergeValidated(*delta.Metadata)
	}
	r.finishReason = delta.FinishReason
	if len(delta.Parts) == 0 && len(delta.MessageMetadata) == 0 {
		return
	}
	mergeValidatedMetadata(&r.messageMetadata, delta.MessageMetadata)
	for _, part := range delta.Parts {
		r.mergePart(part)
	}
}

func (r *ResponseAccumulator) mergePart(delta PartDelta) {
	position, exists := r.positions[delta.Index]
	if !exists {
		position = len(r.parts)
		if r.positions == nil {
			r.positions = make(map[int]int)
		}
		r.positions[delta.Index] = position
		part := Part{Kind: delta.Kind.partKind()}
		if delta.ToolCall != nil {
			part.ToolCall = &ToolCall{ID: delta.ToolCall.ID, Name: delta.ToolCall.Name}
		}
		r.parts = append(r.parts, accumulatedPart{part: part})
	}
	part := &r.parts[position]
	mergeValidatedMetadata(&part.part.Metadata, delta.Metadata)
	switch delta.Kind {
	case PartDeltaText, PartDeltaRefusal:
		part.content = append(part.content, delta.Text...)
	case PartDeltaReasoning:
		part.content = append(part.content, delta.Text...)
		part.part.ReasoningState = append(part.part.ReasoningState, delta.ReasoningState...)
	case PartDeltaToolCall:
		part.content = append(part.content, delta.ToolCall.Arguments...)
	case PartDeltaMedia:
		part.part.Media = delta.Media.Clone()
	case PartDeltaCitation:
		part.part.Citations = append(part.part.Citations, *delta.Citation)
	}
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
