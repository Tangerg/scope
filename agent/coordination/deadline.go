package coordination

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Tangerg/scope/agent/internal/strategy/restore"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

const deadlineStateKind = "coordination.deadline"

type deadlineDefinition struct{ descriptor run.Descriptor }

func newDeadline(name, description string) (*deadlineDefinition, error) {
	schema, err := jsonschema.For[time.Time]()
	if err != nil {
		return nil, err
	}
	descriptor, err := run.NewDescriptor(run.DescriptorConfig{Name: name, Description: description, InputSchema: schema, OutputSchema: schema})
	if err != nil {
		return nil, err
	}
	return &deadlineDefinition{descriptor: descriptor}, nil
}

func (*deadlineDefinition) ChildPrograms() []run.Program[run.Payload, run.Payload] { return nil }

func (d *deadlineDefinition) Descriptor() run.Descriptor {
	if d == nil {
		return run.Descriptor{}
	}
	return d.descriptor
}

func (d *deadlineDefinition) Start(input run.Payload) (run.Execution, error) {
	if !d.valid() {
		return nil, ErrInvalidConfig
	}
	if err := d.descriptor.ValidateInput(input); err != nil {
		return nil, err
	}
	deadline, err := input.Decode[time.Time]()
	if err != nil {
		return nil, fmt.Errorf("%w: decode deadline: %w", run.ErrInvalidPayload, err)
	}
	state := deadlineState{Deadline: deadline, Phase: deadlineReady}
	if err := state.validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", run.ErrInvalidPayload, err)
	}
	return &deadlineExecution{state: state}, nil
}

func (d *deadlineDefinition) Restore(ctx context.Context, state run.ExecutionState) (run.Execution, error) {
	if !d.valid() {
		return nil, ErrInvalidConfig
	}
	decoded, err := restore.Decode(ctx, state, deadlineStateKind, ErrInvalidExecutionState, func(ctx context.Context, decoded deadlineState) error {
		if err := decoded.validate(); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &deadlineExecution{state: decoded}, nil
}

func (d *deadlineDefinition) valid() bool {
	return d != nil && d.descriptor.Valid()
}

type deadlinePhase string

const (
	deadlineReady    deadlinePhase = "ready"
	deadlineAwaiting deadlinePhase = "awaiting_timer"
)

func (d deadlinePhase) valid() bool {
	return d == deadlineReady || d == deadlineAwaiting
}

type deadlineState struct {
	Deadline time.Time     `json:"deadline"`
	Phase    deadlinePhase `json:"phase"`
	Effect   *run.EffectID `json:"effect,omitzero"`
}

// validate states the rule without classifying it: Start reports a breach as
// invalid input and Restore as invalid execution state.
func (d deadlineState) validate() error {
	if d.Deadline.IsZero() {
		return errors.New("absolute deadline is required")
	}
	if (d.Phase == deadlineAwaiting) != (d.Effect != nil) || d.Effect != nil && !d.Effect.Valid() {
		return errors.New("timer effect disagrees with the deadline phase")
	}
	if !d.Phase.valid() {
		return fmt.Errorf("unknown deadline phase %q", d.Phase)
	}
	return nil
}

type deadlineExecution struct{ state deadlineState }

func (d *deadlineExecution) Step(ctx context.Context, turn run.Turn) (run.Transition, error) {
	if err := ctx.Err(); err != nil {
		return run.Transition{}, err
	}
	switch d.state.Phase {
	case deadlineReady:
		if len(turn.Messages) != 0 {
			return run.Transition{}, fmt.Errorf("%w: deadline does not accept external Signals", ErrInvalidProtocol)
		}
		effect, err := newTimerEffect(d.state.Deadline)
		if err != nil {
			return run.Transition{}, err
		}
		d.state.Effect = new(turn.EffectID(0))
		d.state.Phase = deadlineAwaiting
		return run.Yield(0, []run.Effect{effect})
	case deadlineAwaiting:
		return d.acceptTimer(turn.Messages)
	default:
		return run.Transition{}, fmt.Errorf("%w: deadline has no next Step", ErrInvalidProtocol)
	}
}

func (d *deadlineExecution) acceptTimer(messages []run.Message) (run.Transition, error) {
	if len(messages) != 1 {
		return run.Transition{}, fmt.Errorf("%w: deadline requires one timer settlement", ErrInvalidProtocol)
	}
	completion, ok := messages[0].(run.Completion)
	if !ok || d.state.Effect == nil || completion.Effect != *d.state.Effect {
		return run.Transition{}, ErrInvalidProtocol
	}
	settlement := completion.Outcome
	payload := settlement.Payload()
	if settlement.Status() == run.SettlementStatusFailed {
		failure, valid := settlement.Failure()
		if !valid {
			return run.Transition{}, fmt.Errorf("%w: invalid timer failure diagnostic", ErrInvalidProtocol)
		}
		return run.Fail(1, failure)
	}
	if settlement.Status() != run.SettlementStatusSucceeded {
		return run.Transition{}, fmt.Errorf("%w: timer requires a definite settlement", ErrInvalidProtocol)
	}
	if _, err := payload.Decode[timerReached](); err != nil {
		return run.Transition{}, fmt.Errorf("%w: decode timer settlement: %w", ErrInvalidProtocol, err)
	}
	output, err := run.EncodePayload(d.state.Deadline)
	if err != nil {
		return run.Transition{}, err
	}
	return run.Complete(1, output)
}

func (d *deadlineExecution) Snapshot() (run.ExecutionState, error) {
	return run.EncodeExecutionState(deadlineStateKind, d.state)
}

var _ run.Execution = (*deadlineExecution)(nil)

var _ run.Definition = (*deadlineDefinition)(nil)
