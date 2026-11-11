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
	return d.descriptor
}

func (d *deadlineDefinition) Start(input run.Payload) (run.Execution, error) {
	deadline, err := input.Decode[time.Time]()
	if err != nil {
		return nil, fmt.Errorf("%w: decode deadline: %w", run.ErrInvalidPayload, err)
	}
	state := deadlineState{Deadline: deadline}
	if err := state.validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", run.ErrInvalidPayload, err)
	}
	return &deadlineExecution{state: state}, nil
}

func (d *deadlineDefinition) Restore(ctx context.Context, state run.ExecutionState) (run.Execution, error) {
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

// deadlineState is ready until its one timer is armed; the timer Effect alone
// records that it was, so no separate phase can disagree with it.
type deadlineState struct {
	Deadline time.Time    `json:"deadline"`
	Timer    run.EffectID `json:"timer,omitzero"`
}

// validate states the rule without classifying it: Start reports a breach as
// invalid input and Restore as invalid execution state.
func (d deadlineState) validate() error {
	if d.Deadline.IsZero() {
		return errors.New("absolute deadline is required")
	}
	return nil
}

type deadlineExecution struct{ state deadlineState }

func (d *deadlineExecution) Step(ctx context.Context, turn run.Turn) (run.Transition, error) {
	if err := ctx.Err(); err != nil {
		return run.Transition{}, err
	}
	if d.state.Timer.Valid() {
		return d.acceptTimer(turn.Messages)
	}
	if len(turn.Messages) != 0 {
		return run.Transition{}, fmt.Errorf("%w: deadline does not accept external Signals", ErrInvalidProtocol)
	}
	effect, err := newTimerEffect(d.state.Deadline)
	if err != nil {
		return run.Transition{}, err
	}
	d.state.Timer = turn.EffectID(0)
	return run.Yield(0, []run.Effect{effect})
}

func (d *deadlineExecution) acceptTimer(messages []run.Message) (run.Transition, error) {
	if len(messages) != 1 {
		return run.Transition{}, fmt.Errorf("%w: deadline requires one timer settlement", ErrInvalidProtocol)
	}
	completion, ok := messages[0].(run.Completion)
	if !ok || completion.Effect != d.state.Timer {
		return run.Transition{}, ErrInvalidProtocol
	}
	settlement := completion.Outcome
	if failure, failed := settlement.Failure(); failed {
		return run.Fail(1, failure)
	}
	if _, err := settlement.Payload().Decode[timerReached](); err != nil {
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
