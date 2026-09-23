package coordination

import (
	"bytes"
	"context"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
)

const inputGateStateKind = "coordination.input_gate"

// InputGateConfig separates the opening request from the addressed answer.
// The gate returns the original agent.Signal so its identity survives composition.
type InputGateConfig struct {
	Name          string
	Description   string
	RequestSchema agent.Schema
	AnswerSchema  agent.Schema
}

// InputGate publishes its initial input as the wait-opening payload and returns
// one addressed answer as an immutable agent.Signal. Unaddressed inputs are not
// part of this protocol and are rejected at admission, including while the
// final Step runs. A router retains responsibility for rejected input.
type InputGate struct {
	descriptor   agent.Descriptor
	answerSchema agent.Schema
}

func NewInputGate(config InputGateConfig) (*InputGate, error) {
	if !config.AnswerSchema.Valid() {
		return nil, fmt.Errorf("%w: answer schema is required", ErrInvalidConfig)
	}
	outputSchema, err := agent.SchemaFor[agent.Signal]()
	if err != nil {
		return nil, err
	}
	descriptor, err := agent.NewDescriptor(agent.DescriptorConfig{
		Name: config.Name, Description: config.Description,
		InputSchema: config.RequestSchema, OutputSchema: outputSchema,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: input gate descriptor: %w", ErrInvalidConfig, err)
	}
	return &InputGate{descriptor: descriptor, answerSchema: config.AnswerSchema}, nil
}

func (i *InputGate) Descriptor() agent.Descriptor {
	if i == nil {
		return agent.Descriptor{}
	}
	return i.descriptor
}

func (i *InputGate) Start(input agent.Payload) (agent.Execution, error) {
	if !i.valid() {
		return nil, ErrInvalidConfig
	}
	if err := i.descriptor.ValidateInput(input); err != nil {
		return nil, err
	}
	return &inputGateExecution{definition: i, state: inputGateState{Phase: gateReady, Request: input}}, nil
}

func (i *InputGate) Restore(ctx context.Context, state agent.ExecutionState) (agent.Execution, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if !i.valid() {
		return nil, ErrInvalidConfig
	}
	decoded, err := state.Decode[inputGateState](inputGateStateKind)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := decoded.validate(ctx, i); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &inputGateExecution{definition: i, state: decoded}, nil
}

func (i *InputGate) valid() bool {
	return i != nil && i.descriptor.Valid() && i.answerSchema.Valid()
}

type gatePhase string

const (
	gateReady        gatePhase = "ready"
	gateAwaitingOpen gatePhase = "awaiting_open"
	gateWaiting      gatePhase = "waiting"
	gateCompleted    gatePhase = "completed"
)

type inputGateState struct {
	Phase   gatePhase     `json:"phase"`
	Request agent.Payload `json:"request"`
	WaitID  *agent.WaitID `json:"wait_id,omitzero"`
	Answer  *agent.Signal `json:"answer,omitzero"`
}

func (i inputGateState) validate(ctx context.Context, definition *InputGate) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := definition.descriptor.ValidateInput(i.Request); err != nil {
		return fmt.Errorf("%w: opening request: %w", ErrInvalidExecutionState, err)
	}
	switch i.Phase {
	case gateReady, gateAwaitingOpen:
		if i.WaitID != nil || i.Answer != nil {
			return fmt.Errorf("%w: unopened gate retains a wait or answer", ErrInvalidExecutionState)
		}
	case gateWaiting:
		if i.WaitID == nil || !i.WaitID.Valid() {
			return fmt.Errorf("%w: waiting gate requires a valid WaitID", ErrInvalidExecutionState)
		}
		if i.Answer != nil {
			return fmt.Errorf("%w: waiting gate already has an answer", ErrInvalidExecutionState)
		}
	case gateCompleted:
		if i.WaitID == nil || !i.WaitID.Valid() || i.Answer == nil {
			return fmt.Errorf("%w: completed gate requires a wait and answer", ErrInvalidExecutionState)
		}
		if err := i.acceptsAnswer(definition, *i.Answer); err != nil {
			return fmt.Errorf("%w: completed answer: %w", ErrInvalidExecutionState, err)
		}
	default:
		return fmt.Errorf("%w: unknown input gate phase %q", ErrInvalidExecutionState, i.Phase)
	}
	return ctx.Err()
}

func (i inputGateState) acceptsAnswer(definition *InputGate, signal agent.Signal) error {
	waitID, addressed := signal.WaitID()
	if !signal.Valid() || signal.EngineOwned() || i.WaitID == nil || !addressed || waitID != *i.WaitID {
		return fmt.Errorf("%w: answer does not address the input gate", ErrInvalidProtocol)
	}
	payload, err := agent.ParsePayload(signal.Payload())
	if err != nil {
		return err
	}
	if err := definition.answerSchema.Validate(payload.JSON()); err != nil {
		return fmt.Errorf("%w: answer schema: %w", ErrInvalidProtocol, err)
	}
	return nil
}

type inputGateExecution struct {
	definition *InputGate
	state      inputGateState
}

func (i *inputGateExecution) Step(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	transition, err := i.step(ctx, signals)
	if err != nil {
		return agent.Transition{}, agent.ClassifyStepError(err)
	}
	return transition, nil
}

func (i *inputGateExecution) step(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	if err := ctx.Err(); err != nil {
		return agent.Transition{}, err
	}
	switch i.state.Phase {
	case gateReady:
		if len(signals) != 0 {
			return agent.Transition{}, fmt.Errorf("%w: input gate requires an addressed answer", ErrInvalidProtocol)
		}
		key, err := agent.ParseWaitKey("coordination.input")
		if err != nil {
			return agent.Transition{}, err
		}
		effect, err := agent.NewWaitEffect(key, i.state.Request.JSON())
		if err != nil {
			return agent.Transition{}, err
		}
		i.state.Phase = gateAwaitingOpen
		return agent.Continue(0, effect)
	case gateAwaitingOpen:
		if len(signals) == 0 {
			return agent.Transition{}, fmt.Errorf("%w: input gate opening is missing", ErrInvalidProtocol)
		}
		waitID, addressed := signals[0].WaitID()
		if !signals[0].EngineOwned() || !addressed || !bytes.Equal(signals[0].Payload(), i.state.Request.JSON()) {
			return agent.Transition{}, fmt.Errorf("%w: input gate opening disagrees with its request", ErrInvalidProtocol)
		}
		i.state.WaitID = &waitID
		i.state.Phase = gateWaiting
		return agent.Wait(1, waitID)
	case gateWaiting:
		if len(signals) == 0 {
			return agent.Transition{}, fmt.Errorf("%w: input gate answer is missing", ErrInvalidProtocol)
		}
		answer := signals[0]
		if err := i.state.acceptsAnswer(i.definition, answer); err != nil {
			return agent.Transition{}, fmt.Errorf("%w: input gate answer: %w", ErrInvalidProtocol, err)
		}
		i.state.Answer = &answer
		i.state.Phase = gateCompleted
		output, err := agent.EncodePayload(answer)
		if err != nil {
			return agent.Transition{}, err
		}
		return agent.Complete(1, output)
	default:
		return agent.Transition{}, fmt.Errorf("%w: input gate has no next Step", ErrInvalidProtocol)
	}
}

func (i *inputGateExecution) Snapshot() (agent.ExecutionState, error) {
	return agent.EncodeExecutionState(inputGateStateKind, i.state)
}

var _ agent.Execution = (*inputGateExecution)(nil)

var _ agent.Definition = (*InputGate)(nil)
