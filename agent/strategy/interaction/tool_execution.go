package interaction

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
)

const toolExecutionStateKind = "interaction.tool_call"

type toolPhase string

const (
	toolReady            toolPhase = "ready"
	toolAwaitingResult   toolPhase = "awaiting_result"
	toolAwaitingWaitOpen toolPhase = "awaiting_wait_open"
	toolWaitingInput     toolPhase = "waiting_input"
	toolCompleted        toolPhase = "completed"
)

type toolExecutionState struct {
	Phase      toolPhase       `json:"phase"`
	Call       toolCall        `json:"call"`
	Checkpoint *toolCheckpoint `json:"checkpoint,omitempty"`
	WaitID     *agent.WaitID   `json:"wait_id,omitempty"`
	Result     *toolCallResult `json:"result,omitempty"`
}

func (t toolExecutionState) validate() error {
	if err := t.Call.validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
	}
	if t.Checkpoint != nil {
		if err := t.Checkpoint.validate(); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
		}
	}
	if t.Result != nil {
		if err := t.Result.validateCall(t.Call.Call); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
		}
	}

	valid := false
	switch t.Phase {
	case toolReady:
		valid = t.Checkpoint == nil && t.WaitID == nil && t.Result == nil
	case toolAwaitingResult:
		valid = t.WaitID == nil && t.Result == nil
	case toolAwaitingWaitOpen:
		valid = t.Checkpoint != nil && t.WaitID == nil && t.Result == nil
	case toolWaitingInput:
		valid = t.Checkpoint != nil && t.WaitID != nil && t.WaitID.Valid() && t.Result == nil
	case toolCompleted:
		valid = t.Checkpoint == nil && t.WaitID == nil && t.Result != nil
	}
	if !valid {
		return fmt.Errorf("%w: Tool phase disagrees with its continuation", ErrInvalidExecutionState)
	}
	return nil
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

func (t *toolDefinition) Start(input agent.Input) (agent.Execution, error) {
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

func (t *toolDefinition) Restore(state agent.ExecutionState) (agent.Execution, error) {
	decoded, err := decodeToolState(state)
	if err != nil {
		return nil, err
	}
	return &toolExecution{state: decoded}, nil
}

func decodeToolState(state agent.ExecutionState) (toolExecutionState, error) {
	if state.Kind() != toolExecutionStateKind {
		return toolExecutionState{}, ErrInvalidExecutionState
	}
	var decoded toolExecutionState
	if err := jsonv2.Unmarshal(state.Payload(), &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return toolExecutionState{}, fmt.Errorf("%w: Tool state: %w", ErrInvalidExecutionState, err)
	}
	if err := decoded.validate(); err != nil {
		return toolExecutionState{}, err
	}
	return decoded, nil
}

type toolExecution struct{ state toolExecutionState }

func (t *toolExecution) Snapshot() (agent.ExecutionState, error) {
	payload, err := json.Marshal(t.state)
	if err != nil {
		return agent.ExecutionState{}, err
	}
	return agent.NewExecutionState(toolExecutionStateKind, payload)
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
	envelope, err := decodeSignal(signal.Payload())
	if err != nil {
		return agent.Transition{}, err
	}
	switch t.state.Phase {
	case toolAwaitingResult:
		if envelope.Operation != operationToolCall {
			return agent.Transition{}, ErrInvalidExecutionState
		}
		return t.acceptResult(*envelope.ToolResult)
	case toolAwaitingWaitOpen:
		if envelope.Operation != operationWaitOpened {
			return agent.Transition{}, ErrInvalidExecutionState
		}
		waitID, addressed := signal.WaitID()
		if !addressed || !t.state.Checkpoint.InputRequest.equal(*envelope.WaitOpened) {
			return agent.Transition{}, ErrInvalidExecutionState
		}
		t.state.WaitID = &waitID
		t.state.Phase = toolWaitingInput
		return agent.Wait(1, waitID)
	case toolWaitingInput:
		waitID, addressed := signal.WaitID()
		if envelope.Operation != operationInputResponse || !addressed || waitID != *t.state.WaitID {
			return agent.Transition{}, ErrInvalidExecutionState
		}
		return t.request(1, toolDispatchRequest{
			Invocation: t.state.Call,
			Resume:     &toolResume{Checkpoint: *t.state.Checkpoint, InputResponse: envelope.InputResponse},
		})
	default:
		return agent.Transition{}, ErrInvalidExecutionState
	}
}

func (t *toolExecution) request(consumed uint32, call toolDispatchRequest) (agent.Transition, error) {
	envelope, err := newToolEffect(call)
	if err != nil {
		return agent.Transition{}, err
	}
	payload, err := encodeProtocol(envelope)
	if err != nil {
		return agent.Transition{}, err
	}
	effect, err := agent.NewDispatcherEffect(payload)
	if err != nil {
		return agent.Transition{}, err
	}
	t.state.WaitID = nil
	t.state.Phase = toolAwaitingResult
	return agent.Continue(consumed, effect)
}

func (t *toolExecution) acceptResult(outcome toolDispatchResult) (agent.Transition, error) {
	if checkpoint := outcome.Checkpoint; checkpoint != nil {
		previous := uint32(0)
		if t.state.Checkpoint != nil {
			previous = t.state.Checkpoint.PauseCount
		}
		if previous == ^uint32(0) || checkpoint.PauseCount != previous+1 {
			return agent.Transition{}, ErrInvalidExecutionState
		}
		payload, err := encodeProtocol(signalEnvelope{Operation: operationWaitOpened, WaitOpened: &checkpoint.InputRequest})
		if err != nil {
			return agent.Transition{}, err
		}
		key, err := t.state.Call.checkpointWaitKey(checkpoint.PauseCount)
		if err != nil {
			return agent.Transition{}, err
		}
		effect, err := agent.RequestWait(key, payload)
		if err != nil {
			return agent.Transition{}, err
		}
		t.state.Checkpoint = checkpoint
		t.state.Phase = toolAwaitingWaitOpen
		return agent.Continue(1, effect)
	}
	result := outcome.Completion
	if err := result.validateCall(t.state.Call.Call); err != nil {
		return agent.Transition{}, fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
	}
	output, err := agent.EncodeOutput(result)
	if err != nil {
		return agent.Transition{}, err
	}
	t.state.Checkpoint = nil
	t.state.WaitID = nil
	t.state.Result = result
	t.state.Phase = toolCompleted
	return agent.Complete(1, output)
}
