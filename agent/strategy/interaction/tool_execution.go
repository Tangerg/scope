package interaction

import (
	"context"
	jsonv2 "encoding/json/v2"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/internal/restore"
	"github.com/Tangerg/scope/agent/strategy/internal/stepfail"
)

const toolExecutionStateKind = "interaction.tool_call"

// toolInputWaitKey names the one input wait a Tool child holds open at a time.
// Input waits are sequential: a Step consumes the previous wait's answer before
// it requests the next, so the mailbox never sees this key open twice and the
// wait never needs a per-pause identity.
const toolInputWaitKey = "interaction.tool_input"

type toolPhase string

const (
	toolReady            toolPhase = "ready"
	toolAwaitingResult   toolPhase = "awaiting_result"
	toolAwaitingWaitOpen toolPhase = "awaiting_wait_open"
	toolWaitingInput     toolPhase = "waiting_input"
	toolCompleted        toolPhase = "completed"
)

type toolExecutionState struct {
	Phase toolPhase `json:"phase"`
	Call  toolCall  `json:"call"`
	// InputRequest is the input the Tool is currently waiting on. It exists only
	// while the Tool awaits input and is cleared once copied into the resume
	// Effect, so no stale request outlives the wait it belongs to.
	InputRequest *toolInputRequest `json:"input_request,omitzero"`
	WaitID       *agent.WaitID     `json:"wait_id,omitzero"`
}

func (t toolExecutionState) phase() toolPhase {
	if t.Phase == toolAwaitingWaitOpen && t.WaitID != nil {
		return toolWaitingInput
	}
	return t.Phase
}

func (t toolExecutionState) validate() error {
	if err := t.Call.validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
	}
	if t.InputRequest != nil && !t.InputRequest.valid() {
		return fmt.Errorf("%w: %w", ErrInvalidExecutionState, ErrInvalidToolInputRequest)
	}
	if !t.continuationMatchesPhase() {
		return fmt.Errorf("%w: Tool phase disagrees with its continuation", ErrInvalidExecutionState)
	}
	return nil
}

func (t toolExecutionState) continuationMatchesPhase() bool {
	if t.WaitID != nil && (t.Phase != toolAwaitingWaitOpen || !t.WaitID.Valid()) {
		return false
	}
	switch t.Phase {
	case toolReady, toolCompleted, toolAwaitingResult:
		return t.InputRequest == nil
	case toolAwaitingWaitOpen:
		return t.InputRequest != nil
	default:
		return false
	}
}

type toolDefinition struct{ descriptor agent.Descriptor }

func newToolDefinition(name, description string) (*toolDefinition, error) {
	inputSchema, err := agent.SchemaFor[toolCall]()
	if err != nil {
		return nil, err
	}
	outputSchema, err := agent.SchemaFor[toolCallResult]()
	if err != nil {
		return nil, err
	}
	descriptor, err := agent.NewDescriptor(agent.DescriptorConfig{
		Name: name, Description: description, InputSchema: inputSchema, OutputSchema: outputSchema,
	})
	if err != nil {
		return nil, err
	}
	return &toolDefinition{descriptor: descriptor}, nil
}

func (t *toolDefinition) Descriptor() agent.Descriptor { return t.descriptor }

func (*toolDefinition) ChildDeployments() []agent.Deployment { return nil }

func (t *toolDefinition) Start(input agent.Payload) (agent.Execution, error) {
	call, err := input.Decode[toolCall]()
	if err != nil {
		return nil, fmt.Errorf("%w: Tool input: %w", ErrInvalidInput, err)
	}
	state := toolExecutionState{Phase: toolReady, Call: call}
	if err := state.validate(); err != nil {
		return nil, err
	}
	return &toolExecution{state: state}, nil
}

func (t *toolDefinition) Restore(ctx context.Context, state agent.ExecutionState) (agent.Execution, error) {
	decoded, err := restore.Decode(ctx, state, toolExecutionStateKind, ErrInvalidExecutionState, validateToolState)
	if err != nil {
		return nil, err
	}
	return &toolExecution{state: decoded}, nil
}

// decodeToolState reads a retained child state outside Restore; it applies
// the same decoding and validation without a cancellation point.
func decodeToolState(state agent.ExecutionState) (toolExecutionState, error) {
	return restore.Decode(context.Background(), state, toolExecutionStateKind, ErrInvalidExecutionState, validateToolState)
}

func validateToolState(_ context.Context, state toolExecutionState) error { return state.validate() }

type toolExecution struct{ state toolExecutionState }

func (t *toolExecution) Snapshot() (agent.ExecutionState, error) {
	return agent.EncodeExecutionState(toolExecutionStateKind, t.state)
}

func (t *toolExecution) Step(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	if err := ctx.Err(); err != nil {
		return agent.Transition{}, err
	}
	if t.state.Phase == toolReady {
		if len(signals) != 0 {
			return agent.Transition{}, ErrInvalidExecutionState
		}
		return t.request(0, toolDispatchRequest{Invocation: t.state.Call})
	}
	if len(signals) == 0 {
		return agent.Transition{}, fmt.Errorf("%w: Tool expected a Signal", ErrInvalidExecutionState)
	}
	signal := signals[0]
	if t.state.phase() == toolAwaitingWaitOpen {
		return t.acceptWaitOpened(signal)
	}
	switch t.state.phase() {
	case toolAwaitingResult:
		envelope, hostFailure, err := decodeSettlement(signal)
		if err != nil {
			return agent.Transition{}, err
		}
		if hostFailure != "" {
			return stepfail.Transition(1, agent.FailureKindExternal, failureCodeInteractionHostFailed, hostFailure)
		}
		return t.acceptResult(envelope)
	case toolWaitingInput:
		envelope, err := decodeSignal(signal.Payload())
		if err != nil {
			return agent.Transition{}, err
		}
		return t.acceptInputResponse(signal, envelope)
	default:
		return agent.Transition{}, ErrInvalidExecutionState
	}
}

func (t *toolExecution) acceptWaitOpened(signal agent.Signal) (agent.Transition, error) {
	waitID, err := agent.ParseWaitOpened(signal)
	if err != nil {
		return agent.Transition{}, fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
	}
	t.state.WaitID = &waitID
	return agent.Wait(1, waitID)
}

func (t *toolExecution) acceptInputResponse(signal agent.Signal, envelope signalEnvelope) (agent.Transition, error) {
	waitID, addressed := signal.WaitID()
	if envelope.operation() != operationInputResponse || !addressed || waitID != *t.state.WaitID {
		return agent.Transition{}, ErrInvalidExecutionState
	}
	return t.request(1, toolDispatchRequest{
		Invocation: t.state.Call,
		Resume:     &toolResume{InputRequest: *t.state.InputRequest, InputResponse: envelope.InputResponse},
	})
}

func (t *toolExecution) request(consumed uint32, call toolDispatchRequest) (agent.Transition, error) {
	envelope, err := newToolEffect(call)
	if err != nil {
		return agent.Transition{}, err
	}
	payload, err := jsonv2.Marshal(envelope, jsonv2.Deterministic(true))
	if err != nil {
		return agent.Transition{}, err
	}
	effect, err := agent.NewDispatcherEffect(payload)
	if err != nil {
		return agent.Transition{}, err
	}
	t.state.InputRequest = nil
	t.state.WaitID = nil
	t.state.Phase = toolAwaitingResult
	return agent.Continue(consumed, effect)
}

func (t *toolExecution) acceptResult(envelope signalEnvelope) (agent.Transition, error) {
	if envelope.operation() != operationToolCall {
		return agent.Transition{}, ErrInvalidExecutionState
	}
	outcome := envelope.ToolResult
	if outcome.InputRequest != nil {
		return t.openInputWait(*outcome.InputRequest)
	}
	result := outcome.Completion
	if err := result.validateCall(t.state.Call.Call); err != nil {
		return agent.Transition{}, fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
	}
	output, err := agent.EncodePayload(result)
	if err != nil {
		return agent.Transition{}, err
	}
	t.state.InputRequest = nil
	t.state.WaitID = nil
	t.state.Phase = toolCompleted
	return agent.Complete(1, output)
}

func (t *toolExecution) openInputWait(request toolInputRequest) (agent.Transition, error) {
	key, err := agent.ParseWaitKey(toolInputWaitKey)
	if err != nil {
		return agent.Transition{}, err
	}
	effect, err := agent.NewWaitEffect(key)
	if err != nil {
		return agent.Transition{}, err
	}
	t.state.InputRequest = &request
	t.state.Phase = toolAwaitingWaitOpen
	return agent.Continue(1, effect)
}

var _ agent.Definition = (*toolDefinition)(nil)
