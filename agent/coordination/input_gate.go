package coordination

import (
	"context"
	"fmt"

	"github.com/Tangerg/scope/agent/internal/strategy/restore"
	"github.com/Tangerg/scope/agent/run"
)

const (
	inputGateStateKind = "coordination.input_gate"
	inputGateWaitKey   = "coordination.input"
)

type inputGateDefinition struct {
	descriptor run.Descriptor
	wait       run.Wait
}

func newInputGate(name, description string, policy InputGate) (*inputGateDefinition, error) {
	key, err := run.ParseWaitKey(inputGateWaitKey)
	if err != nil {
		return nil, err
	}
	wait := run.Wait{Key: key, AnswerSchema: policy.AnswerSchema, MaxAnswerBytes: policy.MaxAnswerBytes}
	if !wait.Valid() || !wait.AnswerSchema.Valid() {
		return nil, ErrInvalidConfig
	}
	descriptor, err := run.NewDescriptor(run.DescriptorConfig{Name: name, Description: description, InputSchema: policy.RequestSchema, OutputSchema: wait.AnswerSchema})
	if err != nil {
		return nil, err
	}
	return &inputGateDefinition{descriptor: descriptor, wait: wait}, nil
}

func (i *inputGateDefinition) Descriptor() run.Descriptor                           { return i.descriptor }
func (*inputGateDefinition) ChildPrograms() []run.Program[run.Payload, run.Payload] { return nil }

func (i *inputGateDefinition) Start(input run.Payload) (run.Execution, error) {
	return &inputGateExecution{definition: i, state: inputGateState{Request: input, Phase: inputGateReady}}, nil
}

func (i *inputGateDefinition) Restore(ctx context.Context, state run.ExecutionState) (run.Execution, error) {
	value, err := restore.Decode(ctx, state, inputGateStateKind, ErrInvalidExecutionState, func(ctx context.Context, value inputGateState) error { return value.validate(ctx, i) })
	if err != nil {
		return nil, err
	}
	return &inputGateExecution{definition: i, state: value}, nil
}

type inputGatePhase string

const (
	inputGateReady   inputGatePhase = "ready"
	inputGateWaiting inputGatePhase = "waiting"
)

type inputGateState struct {
	Request run.Payload    `json:"request,omitzero"`
	Phase   inputGatePhase `json:"phase"`
}

func (i inputGateState) validate(ctx context.Context, definition *inputGateDefinition) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if i.Phase != inputGateReady && i.Phase != inputGateWaiting {
		return ErrInvalidExecutionState
	}
	if err := definition.descriptor.ValidateInput(i.Request); err != nil {
		return fmt.Errorf("%w: request: %w", ErrInvalidExecutionState, err)
	}
	return nil
}

type inputGateExecution struct {
	definition *inputGateDefinition
	state      inputGateState
}

func (i *inputGateExecution) Snapshot() (run.ExecutionState, error) {
	return run.EncodeExecutionState(inputGateStateKind, i.state)
}

func (i *inputGateExecution) Step(ctx context.Context, turn run.Turn) (run.Transition, error) {
	if err := ctx.Err(); err != nil {
		return run.Transition{}, err
	}
	if i.state.Phase == inputGateReady {
		if len(turn.Messages) != 0 {
			return run.Transition{}, ErrInvalidProtocol
		}
		i.state.Phase = inputGateWaiting
		wait := i.definition.wait
		wait.Question = i.state.Request
		return run.Yield(0, nil, wait)
	}
	if len(turn.Messages) != 1 {
		return run.Transition{}, ErrInvalidProtocol
	}
	answer, ok := turn.Messages[0].(run.Signal)
	if !ok || answer.Wait != i.definition.wait.Key {
		return run.Transition{}, fmt.Errorf("%w: answer does not address input gate", ErrInvalidProtocol)
	}
	return run.Complete(1, answer.Payload)
}
