package interaction

import (
	"context"
	"errors"
	"fmt"
	"slices"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

type invocationContextKey uint8

const (
	modelInvocationContextKey invocationContextKey = iota
	toolInvocationContextKey
)

// ModelInvocation is the immutable execution attribution of one actual model
// call. It contains no Engine handle or Host metadata.
type ModelInvocation struct {
	relation              agent.ProcessRelation
	deploymentRef         agent.DeploymentRef
	effectID              agent.EffectID
	stepSequence          uint64
	modelCallSequence     uint32
	appliedSteerSignalIDs []agent.SignalID
}

// Relation returns the Process tree location that owns the model call.
func (m ModelInvocation) Relation() agent.ProcessRelation { return m.relation }

// DeploymentRef returns the exact Interaction binding that owns the model call.
func (m ModelInvocation) DeploymentRef() agent.DeploymentRef {
	return m.deploymentRef
}

// EffectID returns the stable model Effect identity.
func (m ModelInvocation) EffectID() agent.EffectID { return m.effectID }

// StepSequence returns the one-based Process Step that declared the model Effect.
func (m ModelInvocation) StepSequence() uint64 { return m.stepSequence }

// ModelCallSequence returns the one-based model call position in this Interaction.
func (m ModelInvocation) ModelCallSequence() uint32 {
	return m.modelCallSequence
}

// AppliedSteerSignalIDs returns the ordered identities of steer Signals whose
// messages were first made visible to this exact model request. The returned
// slice is independently owned. An empty slice means the request applied no new
// steer input; previously applied messages may still remain in WorkingContext.
func (m ModelInvocation) AppliedSteerSignalIDs() []agent.SignalID {
	return slices.Clone(m.appliedSteerSignalIDs)
}

func (m ModelInvocation) Valid() bool {
	return m.relation.Valid() && m.deploymentRef.Valid() &&
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
	relation          agent.ProcessRelation
	deploymentRef     agent.DeploymentRef
	effectID          agent.EffectID
	stepSequence      uint64
	modelCallSequence uint32
	toolCallIndex     uint32
	toolCall          chat.ToolCall
}

// Relation returns the Process tree location that owns the Tool call.
func (t ToolInvocation) Relation() agent.ProcessRelation { return t.relation }

// DeploymentRef returns the exact ToolSet binding that owns the Tool call.
func (t ToolInvocation) DeploymentRef() agent.DeploymentRef {
	return t.deploymentRef
}

// EffectID returns the stable identity of this individual Tool Effect.
func (t ToolInvocation) EffectID() agent.EffectID { return t.effectID }

// StepSequence returns the one-based Tool child Step that declared this attempt.
func (t ToolInvocation) StepSequence() uint64 { return t.stepSequence }

// ModelCallSequence returns the one-based model call that requested the Tool.
func (t ToolInvocation) ModelCallSequence() uint32 {
	return t.modelCallSequence
}

// ToolCallIndex returns the zero-based ToolCall position in the model response.
func (t ToolInvocation) ToolCallIndex() uint32 { return t.toolCallIndex }

// ToolCall returns the exact model ToolCall value being executed.
func (t ToolInvocation) ToolCall() chat.ToolCall { return t.toolCall }

// ModelResult maps the executable Tool's Go return values onto the exact
// provider-neutral ToolResult consumed by Interaction. Invalid output becomes
// an error ToolResult. present=false means the cause belongs to the host or
// control plane and must not enter model context.
func (t ToolInvocation) ModelResult(output chat.ToolOutput, cause error) (result chat.ToolResult, present bool) {
	if !t.Valid() {
		return chat.ToolResult{}, false
	}
	call := t.toolCall
	if cause == nil {
		if err := output.Validate(); err != nil {
			cause = fmt.Errorf("tool returned invalid output: %w", err)
		} else {
			return chat.ToolResult{ID: call.ID, Name: call.Name, Output: output.Clone()}, true
		}
	}
	if isHostOrContextError(cause) {
		return chat.ToolResult{}, false
	}
	if _, inputRequired := errors.AsType[*ToolInputRequiredError](cause); inputRequired {
		return chat.ToolResult{}, false
	}
	if errors.Is(cause, tool.ErrAuthorizationDenied) {
		return rejectedToolResult(call, fmt.Sprintf("tool %q is not authorized", call.Name)), true
	}
	if failure, ok := errors.AsType[*tool.Failure](cause); ok {
		return chat.ToolResult{ID: call.ID, Name: call.Name, Output: failure.Output(), IsError: true}, true
	}
	return chat.ToolResult{
		ID: call.ID, Name: call.Name,
		Output:  chat.NewTextToolOutput(fmt.Sprintf("error: tool %q failed: %s", call.Name, boundedDiagnostic(cause.Error()))),
		IsError: true,
	}, true
}

func rejectedToolResult(call chat.ToolCall, diagnostic string) chat.ToolResult {
	return chat.ToolResult{
		ID: call.ID, Name: call.Name, IsError: true,
		Output: chat.NewTextToolOutput("error: " + diagnostic),
	}
}

func (t ToolInvocation) Valid() bool {
	return t.relation.Valid() && t.deploymentRef.Valid() &&
		t.effectID.Valid() && t.stepSequence > 0 &&
		t.modelCallSequence > 0 && t.toolCall.Validate() == nil
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

func modelInvocationFromRequest(
	request agent.EffectRequest,
	modelCallSequence uint32,
	appliedSteerSignalIDs []agent.SignalID,
) ModelInvocation {
	return ModelInvocation{
		relation: request.Relation(), deploymentRef: request.DeploymentRef(),
		effectID: request.ID(), stepSequence: request.StepSequence(),
		modelCallSequence:     modelCallSequence,
		appliedSteerSignalIDs: slices.Clone(appliedSteerSignalIDs),
	}
}

func toolInvocationFromRequest(
	request agent.EffectRequest,
	modelCallSequence uint32,
	toolCallIndex uint32,
	toolCall chat.ToolCall,
) ToolInvocation {
	return ToolInvocation{
		relation: request.Relation(), deploymentRef: request.DeploymentRef(),
		effectID: request.ID(), stepSequence: request.StepSequence(),
		modelCallSequence: modelCallSequence, toolCallIndex: toolCallIndex,
		toolCall: toolCall,
	}
}

func withModelInvocation(ctx context.Context, invocation ModelInvocation) context.Context {
	return context.WithValue(ctx, modelInvocationContextKey, invocation)
}

func withToolInvocation(ctx context.Context, invocation ToolInvocation) context.Context {
	return context.WithValue(ctx, toolInvocationContextKey, invocation)
}
