package coordination

import (
	"context"
	"fmt"
	"slices"

	"github.com/Tangerg/scope/agent/internal/strategy/child"
	"github.com/Tangerg/scope/agent/internal/strategy/restore"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

const (
	firstSuccessStateKind = "coordination.first_success"
	competitionWonReason  = "another candidate satisfied the competition"
)

type SuccessPredicate func(context.Context, run.ChildKey, run.Termination) (bool, error)

type firstSuccessDefinition struct {
	descriptor run.Descriptor
	policy     FirstSuccess
}

func newFirstSuccess(name, description string, policy FirstSuccess) (*firstSuccessDefinition, error) {
	if policy.MaxCandidates == 0 || policy.Accept == nil || policy.Revision == "" {
		return nil, ErrInvalidConfig
	}
	input, err := jsonschema.For[[]run.ChildSpec]()
	if err != nil {
		return nil, err
	}
	output, err := jsonschema.For[FirstSuccessResult]()
	if err != nil {
		return nil, err
	}
	descriptor, err := run.NewDescriptor(run.DescriptorConfig{Name: name, Description: description, InputSchema: input, OutputSchema: output})
	if err != nil {
		return nil, err
	}
	return &firstSuccessDefinition{descriptor: descriptor, policy: policy}, nil
}

func (f *firstSuccessDefinition) Descriptor() run.Descriptor                           { return f.descriptor }
func (*firstSuccessDefinition) ChildPrograms() []run.Program[run.Payload, run.Payload] { return nil }

func (f *firstSuccessDefinition) Start(input run.Payload) (run.Execution, error) {
	candidates, err := input.Decode[[]run.ChildSpec]()
	if err != nil {
		return nil, err
	}
	state := firstSuccessState{Candidates: candidates}
	if err := state.validate(context.Background(), f.policy.MaxCandidates); err != nil {
		return nil, err
	}
	return &firstSuccessExecution{definition: f, state: state}, nil
}

func (f *firstSuccessDefinition) Restore(ctx context.Context, state run.ExecutionState) (run.Execution, error) {
	value, err := restore.Decode(ctx, state, firstSuccessStateKind, ErrInvalidExecutionState, func(ctx context.Context, value firstSuccessState) error {
		return value.validate(ctx, f.policy.MaxCandidates)
	})
	if err != nil {
		return nil, err
	}
	return &firstSuccessExecution{definition: f, state: value}, nil
}

type firstSuccessExecution struct {
	definition *firstSuccessDefinition
	state      firstSuccessState
}

func (f *firstSuccessExecution) Snapshot() (run.ExecutionState, error) {
	return run.EncodeExecutionState(firstSuccessStateKind, f.state)
}

func (f *firstSuccessExecution) Step(ctx context.Context, turn run.Turn) (run.Transition, error) {
	if err := ctx.Err(); err != nil {
		return run.Transition{}, err
	}
	if len(f.state.Calls) == 0 {
		return f.start(turn)
	}
	for _, message := range turn.Messages {
		if err := f.accept(message); err != nil {
			return run.Transition{}, err
		}
	}
	consumed := uint32(len(turn.Messages))
	if f.state.Winner == nil && f.state.admitted() {
		for index, call := range f.state.Calls {
			if call.Termination == nil || call.Termination.Status() != run.StatusCompleted {
				continue
			}
			accepted, err := f.definition.policy.Accept(ctx, f.state.Candidates[index].Key, *call.Termination)
			if err != nil {
				return run.Transition{}, err
			}
			if accepted {
				f.state.Winner = new(uint32(index))
				return f.cancelRemaining(turn, consumed)
			}
		}
	}
	if !f.state.drained() {
		return run.Yield(consumed, nil)
	}
	return f.complete(consumed)
}

func (f *firstSuccessExecution) start(turn run.Turn) (run.Transition, error) {
	if len(turn.Messages) != 0 {
		return run.Transition{}, ErrInvalidProtocol
	}
	effects := make([]run.Effect, len(f.state.Candidates))
	calls := make([]child.Invocation, len(effects))
	for index, candidate := range f.state.Candidates {
		invocation, effect, err := child.Start(turn, uint32(index), candidate)
		if err != nil {
			return run.Transition{}, err
		}
		calls[index], effects[index] = invocation, effect
	}
	f.state.Calls = calls
	return run.Yield(0, effects)
}

func (f *firstSuccessExecution) accept(message run.Message) error {
	if completion, ok := message.(run.Completion); ok {
		if index := slices.Index(f.state.Cancellations, completion.Effect); index >= 0 {
			if completion.Outcome.Status() != run.SettlementStatusSucceeded {
				return fmt.Errorf("%w: competition cancellation failed", ErrInvalidProtocol)
			}
			f.state.Cancellations = slices.Delete(f.state.Cancellations, index, index+1)
			return nil
		}
	}
	for index := range f.state.Calls {
		if f.state.Calls[index].Done() {
			continue
		}
		accepted, err := f.state.Calls[index].Accept(message)
		if err != nil {
			return err
		}
		if accepted {
			return nil
		}
	}
	return ErrInvalidProtocol
}

func (f *firstSuccessExecution) cancelRemaining(turn run.Turn, consumed uint32) (run.Transition, error) {
	var effects []run.Effect
	for _, call := range f.state.Calls {
		if call.Process == nil {
			continue
		}
		effect, err := run.NewChildCancelEffect(*call.Process, competitionWonReason)
		if err != nil {
			return run.Transition{}, err
		}
		f.state.Cancellations = append(f.state.Cancellations, turn.EffectID(uint32(len(effects))))
		effects = append(effects, effect)
	}
	if len(effects) == 0 && f.state.drained() {
		return f.complete(consumed)
	}
	return run.Yield(consumed, effects)
}

func (f *firstSuccessExecution) complete(consumed uint32) (run.Transition, error) {
	output, err := run.EncodePayload(f.state.result())
	if err != nil {
		return run.Transition{}, err
	}
	return run.Complete(consumed, output)
}
