package coordination

import (
	"context"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/agent/internal/strategy/restore"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

const (
	inputGateStateKind = "coordination.input_gate"
	inputGateWaitKey   = "coordination.input"
)

// InputGateConfig separates the opening request from the addressed answer.
// The gate returns the original run.Signal so its identity survives composition.
type InputGateConfig struct {
	Name          string
	Description   string
	RequestSchema jsonschema.Schema
	AnswerSchema  jsonschema.Schema
}

// InputGate retains its initial request in Strategy state, opens an external
// wait, and returns one addressed answer as an immutable run.Signal. The
// opening only acknowledges its WaitID. Unaddressed inputs are not
// part of this protocol and are rejected at admission, including while the
// final Step runs. A router retains responsibility for rejected input.
type InputGate struct {
	descriptor   run.Descriptor
	answerSchema jsonschema.Schema
}

func NewInputGate(config InputGateConfig) (*InputGate, error) {
	if !config.AnswerSchema.Valid() {
		return nil, fmt.Errorf("%w: answer schema is required", ErrInvalidConfig)
	}
	outputSchema, err := jsonschema.For[run.Signal]()
	if err != nil {
		return nil, err
	}
	descriptor, err := run.NewDescriptor(run.DescriptorConfig{
		Name: config.Name, Description: config.Description,
		InputSchema: config.RequestSchema, OutputSchema: outputSchema,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: input gate descriptor: %w", ErrInvalidConfig, err)
	}
	return &InputGate{descriptor: descriptor, answerSchema: config.AnswerSchema}, nil
}

func (*InputGate) ChildPrograms() []run.Program[run.Payload, run.Payload] { return nil }

func (i *InputGate) Descriptor() run.Descriptor {
	if i == nil {
		return run.Descriptor{}
	}
	return i.descriptor
}

func (i *InputGate) Start(input run.Payload) (run.Execution, error) {
	if !i.valid() {
		return nil, ErrInvalidConfig
	}
	if err := i.descriptor.ValidateInput(input); err != nil {
		return nil, err
	}
	return &inputGateExecution{definition: i, state: inputGateState{Phase: gateReady, Request: input}}, nil
}

func (i *InputGate) Restore(ctx context.Context, state run.ExecutionState) (run.Execution, error) {
	if !i.valid() {
		return nil, ErrInvalidConfig
	}
	decoded, err := restore.Decode(ctx, state, inputGateStateKind, ErrInvalidExecutionState, func(ctx context.Context, decoded inputGateState) error {
		return decoded.validate(ctx, i)
	})
	if err != nil {
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
	Phase   gatePhase   `json:"phase"`
	Request run.Payload `json:"request"`
	WaitID  *run.WaitID `json:"wait_id,omitzero"`
}

func (i inputGateState) phase() gatePhase {
	if i.Phase == gateAwaitingOpen && i.WaitID != nil {
		return gateWaiting
	}
	return i.Phase
}

func (i inputGateState) validate(ctx context.Context, definition *InputGate) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := definition.descriptor.ValidateInput(i.Request); err != nil {
		return fmt.Errorf("%w: opening request: %w", ErrInvalidExecutionState, err)
	}
	if i.WaitID != nil && !i.WaitID.Valid() {
		return fmt.Errorf("%w: invalid gate wait", ErrInvalidExecutionState)
	}
	switch i.Phase {
	case gateReady:
		if i.WaitID != nil {
			return fmt.Errorf("%w: unopened gate retains a wait", ErrInvalidExecutionState)
		}
	case gateAwaitingOpen:
	case gateCompleted:
		// A completed gate's answer, including the wait it addressed, is the
		// Runtime-owned Output, never repeated here.
		if i.WaitID != nil {
			return fmt.Errorf("%w: completed gate repeats its Output", ErrInvalidExecutionState)
		}
	default:
		return fmt.Errorf("%w: unknown input gate phase %q", ErrInvalidExecutionState, i.Phase)
	}
	return ctx.Err()
}

// acceptsAnswer confirms that a signal addresses this gate's open wait. The
// answer schema and the deliverable-output capacity are the wait's acceptance
// contract, enforced by the mailbox at admission, so this Step trusts the
// admitted answer rather than revalidating it. Its one caller, acceptAnswer,
// classifies a failure as a protocol violation.
func (i inputGateState) acceptsAnswer(signal run.Signal) error {
	waitID, addressed := signal.WaitID()
	_, err := signal.Frame[run.ExternalInput]()
	if err != nil || i.WaitID == nil || !addressed || waitID != *i.WaitID {
		return errors.New("answer does not address the input gate")
	}
	return nil
}

type inputGateExecution struct {
	definition *InputGate
	state      inputGateState
}

func (i *inputGateExecution) Step(ctx context.Context, signals []run.Signal) (run.Transition, error) {
	if err := ctx.Err(); err != nil {
		return run.Transition{}, err
	}
	switch i.state.phase() {
	case gateReady:
		return i.open(signals)
	case gateAwaitingOpen:
		return i.acceptOpening(signals)
	case gateWaiting:
		return i.acceptAnswer(signals)
	default:
		return run.Transition{}, fmt.Errorf("%w: input gate has no next Step", ErrInvalidProtocol)
	}
}

func (i *inputGateExecution) open(signals []run.Signal) (run.Transition, error) {
	if len(signals) != 0 {
		return run.Transition{}, fmt.Errorf("%w: input gate requires an addressed answer", ErrInvalidProtocol)
	}
	key, err := run.ParseWaitKey(inputGateWaitKey)
	if err != nil {
		return run.Transition{}, err
	}
	// The wait owns the answer contract so the mailbox rejects a schema-invalid or
	// undeliverable answer at admission and keeps the wait open; AnswerIsOutput
	// closes the accept/deliver gap for the Signal this gate returns verbatim.
	spec, err := run.NewExternalWaitSpec(run.ExternalWaitSpecConfig{
		Key:            key,
		AnswerSchema:   i.definition.answerSchema,
		AnswerIsOutput: true,
	})
	if err != nil {
		return run.Transition{}, err
	}
	effect, err := run.NewWaitEffect(spec)
	if err != nil {
		return run.Transition{}, err
	}
	i.state.Phase = gateAwaitingOpen
	return run.Continue(0, effect)
}

func (i *inputGateExecution) acceptOpening(signals []run.Signal) (run.Transition, error) {
	if len(signals) == 0 {
		return run.Transition{}, fmt.Errorf("%w: input gate opening is missing", ErrInvalidProtocol)
	}
	opening, err := signals[0].Frame[run.WaitOpened]()
	if err != nil {
		return run.Transition{}, fmt.Errorf("%w: input gate opening: %w", ErrInvalidProtocol, err)
	}
	waitID := opening.WaitID()
	i.state.WaitID = &waitID
	return run.Wait(1, waitID)
}

func (i *inputGateExecution) acceptAnswer(signals []run.Signal) (run.Transition, error) {
	if len(signals) == 0 {
		return run.Transition{}, fmt.Errorf("%w: input gate answer is missing", ErrInvalidProtocol)
	}
	answer := signals[0]
	if err := i.state.acceptsAnswer(answer); err != nil {
		return run.Transition{}, fmt.Errorf("%w: input gate answer: %w", ErrInvalidProtocol, err)
	}
	output, err := run.EncodePayload(answer)
	if err != nil {
		return run.Transition{}, err
	}
	i.state = inputGateState{Phase: gateCompleted, Request: i.state.Request}
	return run.Complete(1, output)
}

func (i *inputGateExecution) Snapshot() (run.ExecutionState, error) {
	return run.EncodeExecutionState(inputGateStateKind, i.state)
}

var _ run.Execution = (*inputGateExecution)(nil)

var _ run.Definition = (*InputGate)(nil)
