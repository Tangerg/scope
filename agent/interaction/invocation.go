package interaction

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

type ModelInvocation struct {
	request               run.EffectRequest
	modelCallSequence     uint64
	appliedSteerSignalIDs []run.SignalID
}

func (m ModelInvocation) ProcessID() run.ProcessID   { return m.request.ID().Process }
func (m ModelInvocation) Epoch() run.Epoch           { return m.request.Epoch() }
func (m ModelInvocation) ProgramRef() run.ProgramRef { return m.request.ProgramRef() }
func (m ModelInvocation) EffectID() run.EffectID     { return m.request.ID() }
func (m ModelInvocation) ModelCallSequence() uint64  { return m.modelCallSequence }
func (m ModelInvocation) AppliedSteerSignalIDs() []run.SignalID {
	return slices.Clone(m.appliedSteerSignalIDs)
}
func (m ModelInvocation) Valid() bool {
	return m.request.Valid() && m.modelCallSequence > 0 && (len(m.appliedSteerSignalIDs) == 0 || validateSteerSignalIDs(m.appliedSteerSignalIDs) == nil)
}
func ModelInvocationFromContext(ctx context.Context) (ModelInvocation, bool) {
	if ctx == nil {
		return ModelInvocation{}, false
	}
	value, found := ctx.Value(modelInvocationContextKey).(ModelInvocation)
	return value, found && value.Valid()
}

type ToolInvocation struct {
	request run.EffectRequest
	call    toolDispatchRequest
}

func (t ToolInvocation) ProcessID() run.ProcessID   { return t.request.ID().Process }
func (t ToolInvocation) Epoch() run.Epoch           { return t.request.Epoch() }
func (t ToolInvocation) ProgramRef() run.ProgramRef { return t.request.ProgramRef() }
func (t ToolInvocation) EffectID() run.EffectID     { return t.request.ID() }
func (t ToolInvocation) ToolCall() chat.ToolCall    { return t.call.Invocation.Call }
func (t ToolInvocation) Reference() run.EffectID {
	if t.call.Resume != nil {
		return t.call.Resume.Reference
	}
	return t.request.ID()
}
func (t ToolInvocation) Valid() bool {
	return t.request.Valid() && t.call.Invocation.validate() == nil && (t.call.Resume == nil || t.call.Resume.precedes(t.request.ID()))
}
func ToolInvocationFromContext(ctx context.Context) (ToolInvocation, bool) {
	if ctx == nil {
		return ToolInvocation{}, false
	}
	value, found := ctx.Value(toolInvocationContextKey).(ToolInvocation)
	return value, found && value.Valid()
}
func ToolInvocationFromRequest(request run.EffectRequest) (ToolInvocation, error) {
	if !request.Valid() {
		return ToolInvocation{}, ErrInvalidProtocol
	}
	envelope, err := decodeEffect(request.Effect().Payload())
	if err != nil {
		return ToolInvocation{}, err
	}
	if envelope.ToolCall == nil {
		return ToolInvocation{}, fmt.Errorf("%w: tool attribution requires a tool effect", ErrInvalidProtocol)
	}
	invocation := ToolInvocation{request: request, call: *envelope.ToolCall}
	if !invocation.Valid() {
		return ToolInvocation{}, ErrInvalidProtocol
	}
	return invocation, nil
}
func modelInvocationFromRequest(request run.EffectRequest, sequence uint64, signals []run.SignalID) ModelInvocation {
	return ModelInvocation{request: request, modelCallSequence: sequence, appliedSteerSignalIDs: slices.Clone(signals)}
}
func withModelInvocation(ctx context.Context, invocation ModelInvocation) context.Context {
	return context.WithValue(ctx, modelInvocationContextKey, invocation)
}
func withToolInvocation(ctx context.Context, invocation ToolInvocation) context.Context {
	return context.WithValue(ctx, toolInvocationContextKey, invocation)
}
