package agent

import (
	"context"
	"fmt"
	"slices"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
)

type invocationContextKey uint8

const (
	modelInvocationContextKey invocationContextKey = iota
	toolInvocationContextKey
)

// ModelInvocation is the immutable execution attribution of one actual model
// call. It contains no Runtime handle or Host metadata.
type ModelInvocation struct {
	incarnationID         run.TreeIncarnationID
	relation              run.ProcessRelation
	programRef            run.ProgramRef
	effectID              run.EffectID
	attemptID             run.EffectAttemptID
	stepSequence          uint64
	modelCallSequence     uint64
	appliedSteerSignalIDs []run.SignalID
}

// TreeIncarnationID identifies the durable writer activation, when present.
func (m ModelInvocation) TreeIncarnationID() (run.TreeIncarnationID, bool) {
	return m.incarnationID, m.incarnationID.Valid()
}

func (m ModelInvocation) Relation() run.ProcessRelation { return m.relation }

func (m ModelInvocation) ProgramRef() run.ProgramRef {
	return m.programRef
}

func (m ModelInvocation) EffectID() run.EffectID { return m.effectID }

// AttemptID identifies this physical invocation. Logical requests retained in
// snapshots have no attempt identity until the Runtime dispatches them again.
func (m ModelInvocation) AttemptID() (run.EffectAttemptID, bool) {
	return m.attemptID, m.attemptID.Valid()
}

// StepSequence returns the one-based Process Step that declared the model Effect.
func (m ModelInvocation) StepSequence() uint64 { return m.stepSequence }

// ModelCallSequence returns the one-based model call position in this Interaction.
func (m ModelInvocation) ModelCallSequence() uint64 {
	return m.modelCallSequence
}

// AppliedSteerSignalIDs returns the ordered identities of steer Signals whose
// messages were first made visible to this exact model request. The returned
// slice is independently owned. An empty slice means the request applied no new
// steer input; previously applied messages may still remain in WorkingContext.
func (m ModelInvocation) AppliedSteerSignalIDs() []run.SignalID {
	return slices.Clone(m.appliedSteerSignalIDs)
}

func (m ModelInvocation) Valid() bool {
	return m.relation.Valid() && m.programRef.Valid() &&
		m.effectID.Valid() && m.stepSequence > 0 &&
		m.modelCallSequence > 0 &&
		(len(m.appliedSteerSignalIDs) == 0 || validateSteerSignalIDs(m.appliedSteerSignalIDs) == nil)
}

// ModelInvocationFromContext returns the attribution installed only for the
// duration of an Interaction model call. A nil context has no attribution.
func ModelInvocationFromContext(ctx context.Context) (ModelInvocation, bool) {
	if ctx == nil {
		return ModelInvocation{}, false
	}
	invocation, present := ctx.Value(modelInvocationContextKey).(ModelInvocation)
	return invocation, present && invocation.Valid()
}

// ToolInvocation is the immutable execution attribution of one actual Tool
// call. ToolCall is the exact model request being executed.
type ToolInvocation struct {
	incarnationID run.TreeIncarnationID
	relation      run.ProcessRelation
	programRef    run.ProgramRef
	effectID      run.EffectID
	attemptID     run.EffectAttemptID
	stepSequence  uint64
	toolCall      chat.ToolCall
}

// TreeIncarnationID identifies the durable writer activation, when present.
func (t ToolInvocation) TreeIncarnationID() (run.TreeIncarnationID, bool) {
	return t.incarnationID, t.incarnationID.Valid()
}

func (t ToolInvocation) Relation() run.ProcessRelation { return t.relation }

func (t ToolInvocation) ProgramRef() run.ProgramRef {
	return t.programRef
}

func (t ToolInvocation) EffectID() run.EffectID { return t.effectID }

// AttemptID identifies this physical invocation, when dispatched by the Runtime.
func (t ToolInvocation) AttemptID() (run.EffectAttemptID, bool) {
	return t.attemptID, t.attemptID.Valid()
}

// StepSequence returns the one-based Tool child Step that declared this attempt.
func (t ToolInvocation) StepSequence() uint64 { return t.stepSequence }

func (t ToolInvocation) ToolCall() chat.ToolCall { return t.toolCall }

// Reference correlates this attempt with its requesting Interaction's results.
// It comes from the ChildKey an Interaction gives the Tool child, which the
// Runtime keeps unique under the parent. A ToolSet run as a root, or started
// under any other ChildKey, has no reference; its physical invocation
// attribution remains available through this value.
func (t ToolInvocation) Reference() (ToolCallRef, bool) {
	if !t.Valid() {
		return ToolCallRef{}, false
	}
	return toolCallRef(t.relation)
}

func (t ToolInvocation) Valid() bool {
	return t.relation.Valid() && t.programRef.Valid() &&
		t.effectID.Valid() && t.stepSequence > 0 &&
		t.toolCall.Validate() == nil
}

// ToolInvocationFromContext returns the attribution installed only for the
// duration of the exact Interaction Tool call. A nil context has no attribution.
func ToolInvocationFromContext(ctx context.Context) (ToolInvocation, bool) {
	if ctx == nil {
		return ToolInvocation{}, false
	}
	invocation, present := ctx.Value(toolInvocationContextKey).(ToolInvocation)
	return invocation, present && invocation.Valid()
}

// ToolInvocationFromRequest decodes the exact attribution retained by a Tool
// EffectRequest, including requests recovered from TreeSnapshot.EffectRequest.
// Retained requests have no physical AttemptID until dispatched again. This
// operation performs no Tool call and does not resolve an unknown Effect.
func ToolInvocationFromRequest(request run.EffectRequest) (ToolInvocation, error) {
	if !request.Valid() {
		return ToolInvocation{}, fmt.Errorf("%w: invalid Tool request", ErrInvalidProtocol)
	}
	envelope, err := decodeEffect(request.Effect().Payload())
	if err != nil {
		return ToolInvocation{}, err
	}
	if envelope.operation() != operationToolCall {
		return ToolInvocation{}, fmt.Errorf("%w: Tool attribution requires a tool_call", ErrInvalidProtocol)
	}
	call := envelope.ToolCall.Invocation
	return toolInvocationFromRequest(request, call.Call), nil
}

func modelInvocationFromRequest(
	request run.EffectRequest,
	modelCallSequence uint64,
	appliedSteerSignalIDs []run.SignalID,
) ModelInvocation {
	incarnation, _ := request.TreeIncarnationID()
	attempt, _ := request.AttemptID()
	return ModelInvocation{
		incarnationID: incarnation,
		attemptID:     attempt,
		relation:      request.Relation(), programRef: request.ProgramRef(),
		effectID: request.ID(), stepSequence: request.StepSequence(),
		modelCallSequence:     modelCallSequence,
		appliedSteerSignalIDs: slices.Clone(appliedSteerSignalIDs),
	}
}

func toolInvocationFromRequest(request run.EffectRequest, toolCall chat.ToolCall) ToolInvocation {
	incarnation, _ := request.TreeIncarnationID()
	attempt, _ := request.AttemptID()
	return ToolInvocation{
		incarnationID: incarnation,
		attemptID:     attempt,
		relation:      request.Relation(), programRef: request.ProgramRef(),
		effectID: request.ID(), stepSequence: request.StepSequence(),
		toolCall: toolCall,
	}
}

func withModelInvocation(ctx context.Context, invocation ModelInvocation) context.Context {
	return context.WithValue(ctx, modelInvocationContextKey, invocation)
}

func withToolInvocation(ctx context.Context, invocation ToolInvocation) context.Context {
	return context.WithValue(ctx, toolInvocationContextKey, invocation)
}
