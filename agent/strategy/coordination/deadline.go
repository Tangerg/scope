package coordination

import (
	"context"
	"fmt"
	"time"

	agent "github.com/Tangerg/scope/agent"
)

const deadlineStateKind = "coordination.deadline"

// DeadlineConfig names the immutable deadline Definition. Each execution's
// absolute instant is supplied as Input and retained in its state.
type DeadlineConfig struct {
	Name        string
	Description string
}

// Deadline accepts an absolute time.Time and completes with that same instant
// after Timer acknowledges reaching it. Step never reads the clock. Restoration
// retains the absolute deadline rather than restarting a relative delay.
type Deadline struct{ descriptor agent.Descriptor }

func NewDeadline(config DeadlineConfig) (*Deadline, error) {
	schema, err := agent.SchemaFor[time.Time]()
	if err != nil {
		return nil, err
	}
	descriptor, err := agent.NewDescriptor(agent.DescriptorConfig{
		Name: config.Name, Description: config.Description, InputSchema: schema, OutputSchema: schema,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: deadline descriptor: %w", ErrInvalidConfig, err)
	}
	return &Deadline{descriptor: descriptor}, nil
}

func (d *Deadline) Descriptor() agent.Descriptor {
	if d == nil {
		return agent.Descriptor{}
	}
	return d.descriptor
}

func (d *Deadline) Start(input agent.Input) (agent.Execution, error) {
	if d == nil || !d.descriptor.Valid() {
		return nil, ErrInvalidConfig
	}
	if err := d.descriptor.ValidateInput(input); err != nil {
		return nil, err
	}
	deadline, err := input.Decode[time.Time]()
	if err != nil {
		return nil, fmt.Errorf("%w: decode deadline: %w", agent.ErrInvalidInput, err)
	}
	if deadline.IsZero() {
		return nil, fmt.Errorf("%w: absolute deadline is required", agent.ErrInvalidInput)
	}
	return &deadlineExecution{state: deadlineState{Deadline: deadline, Phase: deadlineReady}}, nil
}

func (d *Deadline) Restore(state agent.ExecutionState) (agent.Execution, error) {
	if d == nil || !d.descriptor.Valid() {
		return nil, ErrInvalidConfig
	}
	decoded, err := decodeState[deadlineState](deadlineStateKind, state)
	if err != nil {
		return nil, err
	}
	if !decoded.valid() {
		return nil, fmt.Errorf("%w: deadline or phase is invalid", ErrInvalidState)
	}
	return &deadlineExecution{state: decoded}, nil
}

type deadlinePhase string

const (
	deadlineReady     deadlinePhase = "ready"
	deadlineAwaiting  deadlinePhase = "awaiting_timer"
	deadlineCompleted deadlinePhase = "completed"
)

type deadlineState struct {
	Deadline time.Time     `json:"deadline"`
	Phase    deadlinePhase `json:"phase"`
}

func (d deadlineState) valid() bool {
	return !d.Deadline.IsZero() && (d.Phase == deadlineReady || d.Phase == deadlineAwaiting || d.Phase == deadlineCompleted)
}

type deadlineExecution struct{ state deadlineState }

func (d *deadlineExecution) Step(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	if err := ctx.Err(); err != nil {
		return agent.Transition{}, err
	}
	switch d.state.Phase {
	case deadlineReady:
		if len(signals) != 0 {
			return agent.Transition{}, fmt.Errorf("%w: deadline does not accept external Signals", ErrInvalidProtocol)
		}
		effect, err := newTimerEffect(d.state.Deadline)
		if err != nil {
			return agent.Transition{}, err
		}
		d.state.Phase = deadlineAwaiting
		return agent.Continue(0, effect)
	case deadlineAwaiting:
		if len(signals) != 1 {
			return agent.Transition{}, fmt.Errorf("%w: deadline requires one timer settlement", ErrInvalidProtocol)
		}
		if _, addressed := signals[0].WaitID(); addressed {
			return agent.Transition{}, fmt.Errorf("%w: timer settlement cannot address a wait", ErrInvalidProtocol)
		}
		payload, err := agent.ParseInput(signals[0].Payload())
		if err != nil {
			return agent.Transition{}, err
		}
		result, err := payload.Decode[timerResult]()
		if err != nil {
			return agent.Transition{}, fmt.Errorf("%w: decode timer settlement: %w", ErrInvalidProtocol, err)
		}
		if !result.Deadline.Equal(d.state.Deadline) {
			return agent.Transition{}, fmt.Errorf("%w: timer settlement disagrees with its deadline", ErrInvalidProtocol)
		}
		if !result.Reached {
			failure, failureErr := agent.NewFailure(agent.FailureKindExternal, "coordination.deadline.interrupted", "timer returned before its deadline")
			if failureErr != nil {
				return agent.Transition{}, failureErr
			}
			return agent.Fail(1, failure)
		}
		d.state.Phase = deadlineCompleted
		output, err := agent.EncodeOutput(d.state.Deadline)
		if err != nil {
			return agent.Transition{}, err
		}
		return agent.Complete(1, output)
	default:
		return agent.Transition{}, fmt.Errorf("%w: deadline has no next Step", ErrInvalidProtocol)
	}
}

func (d *deadlineExecution) Snapshot() (agent.ExecutionState, error) {
	return encodeState(deadlineStateKind, d.state)
}
