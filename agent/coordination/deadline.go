package coordination

import (
	"context"
	"fmt"
	"time"

	"github.com/Tangerg/scope/agent/internal/strategy/restore"
	"github.com/Tangerg/scope/agent/internal/strategy/stepfail"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

const deadlineStateKind = "coordination.deadline"

// DeadlineConfig names the immutable deadline Definition. Each execution's
// absolute instant is supplied as Input and retained in its state.
type DeadlineConfig struct {
	Name        string
	Description string
}

// Deadline accepts an absolute time.Time and completes with that same instant
// after DeadlineDispatcher acknowledges reaching it. Step never reads the clock. Restoration
// retains the absolute deadline rather than restarting a relative delay.
type Deadline struct{ descriptor run.Descriptor }

func NewDeadline(config DeadlineConfig) (*Deadline, error) {
	schema, err := jsonschema.For[time.Time]()
	if err != nil {
		return nil, err
	}
	descriptor, err := run.NewDescriptor(run.DescriptorConfig{
		Name: config.Name, Description: config.Description, InputSchema: schema, OutputSchema: schema,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: deadline descriptor: %w", ErrInvalidConfig, err)
	}
	return &Deadline{descriptor: descriptor}, nil
}

func (*Deadline) ChildPrograms() []run.Program[run.Payload, run.Payload] { return nil }

func (d *Deadline) Descriptor() run.Descriptor {
	if d == nil {
		return run.Descriptor{}
	}
	return d.descriptor
}

func (d *Deadline) Start(input run.Payload) (run.Execution, error) {
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

func (d *Deadline) Restore(ctx context.Context, state run.ExecutionState) (run.Execution, error) {
	if !d.valid() {
		return nil, ErrInvalidConfig
	}
	decoded, err := restore.Decode(ctx, state, deadlineStateKind, ErrInvalidExecutionState, func(ctx context.Context, decoded deadlineState) error {
		return decoded.validate()
	})
	if err != nil {
		return nil, err
	}
	return &deadlineExecution{state: decoded}, nil
}

func (d *Deadline) valid() bool {
	return d != nil && d.descriptor.Valid()
}

type deadlinePhase string

const (
	deadlineReady     deadlinePhase = "ready"
	deadlineAwaiting  deadlinePhase = "awaiting_timer"
	deadlineCompleted deadlinePhase = "completed"
)

func (d deadlinePhase) valid() bool {
	return d == deadlineReady || d == deadlineAwaiting || d == deadlineCompleted
}

type deadlineState struct {
	Deadline time.Time     `json:"deadline"`
	Phase    deadlinePhase `json:"phase"`
}

func (d deadlineState) validate() error {
	if d.Deadline.IsZero() {
		return fmt.Errorf("%w: absolute deadline is required", ErrInvalidExecutionState)
	}
	if !d.Phase.valid() {
		return fmt.Errorf("%w: unknown deadline phase %q", ErrInvalidExecutionState, d.Phase)
	}
	return nil
}

type deadlineExecution struct{ state deadlineState }

func (d *deadlineExecution) Step(ctx context.Context, signals []run.Signal) (run.Transition, error) {
	if err := ctx.Err(); err != nil {
		return run.Transition{}, err
	}
	switch d.state.Phase {
	case deadlineReady:
		if len(signals) != 0 {
			return run.Transition{}, fmt.Errorf("%w: deadline does not accept external Signals", ErrInvalidProtocol)
		}
		effect, err := newTimerEffect(d.state.Deadline)
		if err != nil {
			return run.Transition{}, err
		}
		d.state.Phase = deadlineAwaiting
		return run.Continue(0, effect)
	case deadlineAwaiting:
		return d.acceptTimer(signals)
	default:
		return run.Transition{}, fmt.Errorf("%w: deadline has no next Step", ErrInvalidProtocol)
	}
}

func (d *deadlineExecution) acceptTimer(signals []run.Signal) (run.Transition, error) {
	if len(signals) != 1 {
		return run.Transition{}, fmt.Errorf("%w: deadline requires one timer settlement", ErrInvalidProtocol)
	}
	settlement, err := signals[0].Frame[run.Settlement]()
	if err != nil {
		return run.Transition{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	payload, err := run.ParsePayload(settlement.Payload())
	if err != nil {
		return run.Transition{}, fmt.Errorf("%w: timer settlement payload: %w", ErrInvalidProtocol, err)
	}
	if settlement.Status() == run.SettlementStatusFailed {
		diagnostic, decodeErr := payload.Decode[string]()
		if decodeErr != nil || !run.ValidDiagnostic(diagnostic) {
			return run.Transition{}, fmt.Errorf("%w: invalid timer failure diagnostic", ErrInvalidProtocol)
		}
		failure, failureErr := stepfail.Failure(run.FailureKindExternal, failureCodeDeadlineInterrupted, diagnostic)
		if failureErr != nil {
			return run.Transition{}, failureErr
		}
		return run.Fail(1, failure)
	}
	if settlement.Status() != run.SettlementStatusSucceeded {
		return run.Transition{}, fmt.Errorf("%w: timer requires a definite settlement", ErrInvalidProtocol)
	}
	if _, err = payload.Decode[timerReached](); err != nil {
		return run.Transition{}, fmt.Errorf("%w: decode timer settlement: %w", ErrInvalidProtocol, err)
	}
	d.state.Phase = deadlineCompleted
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

var _ run.Definition = (*Deadline)(nil)

const failureCodeDeadlineInterrupted = "coordination.deadline.interrupted"
