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
// part of this protocol. Inputs accepted after its final Step window remain in
// the Process mailbox; a router must account for their disposition separately.
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

func (i *InputGate) Start(input agent.Input) (agent.Execution, error) {
	if !i.valid() {
		return nil, ErrInvalidConfig
	}
	if err := i.descriptor.ValidateInput(input); err != nil {
		return nil, err
	}
	return &inputGateExecution{definition: i, state: inputGateState{Phase: gateReady, Request: input}}, nil
}

func (i *InputGate) Restore(state agent.ExecutionState) (agent.Execution, error) {
	if !i.valid() {
		return nil, ErrInvalidConfig
	}
	decoded, err := decodeState[inputGateState](inputGateStateKind, state)
	if err != nil {
		return nil, err
	}
	if err := decoded.validate(i); err != nil {
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
	Request agent.Input   `json:"request"`
	WaitID  *agent.WaitID `json:"wait_id,omitempty"`
	Answer  *agent.Signal `json:"answer,omitempty"`
}

func (i inputGateState) validate(definition *InputGate) error {
	if err := definition.descriptor.ValidateInput(i.Request); err != nil {
		return fmt.Errorf("%w: opening request: %w", ErrInvalidState, err)
	}
	switch i.Phase {
	case gateReady, gateAwaitingOpen:
		if i.WaitID == nil && i.Answer == nil {
			return nil
		}
	case gateWaiting:
		if i.WaitID != nil && i.WaitID.Valid() && i.Answer == nil {
			return nil
		}
	case gateCompleted:
		if i.WaitID != nil && i.Answer != nil {
			if err := i.acceptsAnswer(definition, *i.Answer); err != nil {
				return fmt.Errorf("%w: completed answer: %w", ErrInvalidState, err)
			}
			return nil
		}
	}
	return fmt.Errorf("%w: invalid input gate phase or answer", ErrInvalidState)
}

func (i inputGateState) acceptsAnswer(definition *InputGate, signal agent.Signal) error {
	waitID, addressed := signal.WaitID()
	if !signal.Valid() || i.WaitID == nil || !addressed || waitID != *i.WaitID {
		return fmt.Errorf("%w: answer does not address the input gate", ErrInvalidProtocol)
	}
	payload, err := agent.ParseInput(signal.Payload())
	if err != nil {
		return err
	}
	if err := definition.answerSchema.ValidateInput(payload); err != nil {
		return fmt.Errorf("%w: answer schema: %w", ErrInvalidProtocol, err)
	}
	return nil
}

type inputGateExecution struct {
	definition *InputGate
	state      inputGateState
}

func (i *inputGateExecution) Step(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
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
		effect, err := agent.RequestWait(key, i.state.Request.JSON())
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
		if !addressed || !bytes.Equal(signals[0].Payload(), i.state.Request.JSON()) {
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
			return agent.Transition{}, err
		}
		i.state.Answer = &answer
		i.state.Phase = gateCompleted
		output, err := agent.EncodeOutput(answer)
		if err != nil {
			return agent.Transition{}, err
		}
		return agent.Complete(1, output)
	default:
		return agent.Transition{}, fmt.Errorf("%w: input gate has no next Step", ErrInvalidProtocol)
	}
}

func (i *inputGateExecution) Snapshot() (agent.ExecutionState, error) {
	return encodeState(inputGateStateKind, i.state)
}
