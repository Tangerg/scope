package coordination

import (
	"context"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
)

const firstSuccessStateKind = "coordination.first_success"

// SuccessPredicate decides whether a completed child satisfies the business
// goal. It runs inside Step and must be bounded, deterministic, side-effect-free,
// and honor ctx cancellation. Non-completed children never satisfy success.
type SuccessPredicate func(ctx context.Context, outcome agent.ChildOutcome) (bool, error)

// FirstSuccessConfig binds a finite candidate bound and pure decision policy.
// The Deployment configuration digest must identify both values.
type FirstSuccessConfig struct {
	Name          string
	Description   string
	MaxCandidates uint32
	Accept        SuccessPredicate
}

// FirstSuccess accepts a non-empty []agent.ChildSpec and runs those candidates
// under one ownership scope. It retains failed starts and observed terminal
// results, chooses the first accepted result in each request-ordered wait
// response, and completes with FirstSuccessResult. An empty Winner means every
// candidate failed to satisfy Accept; it is an explicit business result.
//
// Every candidate's admission and initialization must settle before results are
// considered. A slow later admission can therefore delay acceptance of an
// already completed child. When multiple results are visible together, request
// order determines the winner, independently of their completion timestamps.
//
// Results are observed at the terminal-result boundary. Completion starts
// cancellation of remaining descendants; it does not establish their drain.
type FirstSuccess struct {
	descriptor    agent.Descriptor
	maxCandidates uint32
	accept        SuccessPredicate
}

func NewFirstSuccess(config FirstSuccessConfig) (*FirstSuccess, error) {
	if config.MaxCandidates == 0 || config.Accept == nil {
		return nil, fmt.Errorf("%w: candidate bound and success predicate are required", ErrInvalidConfig)
	}
	inputSchema, err := agent.SchemaFor[[]agent.ChildSpec]()
	if err != nil {
		return nil, err
	}
	outputSchema, err := agent.SchemaFor[FirstSuccessResult]()
	if err != nil {
		return nil, err
	}
	descriptor, err := agent.NewDescriptor(agent.DescriptorConfig{
		Name: config.Name, Description: config.Description, InputSchema: inputSchema, OutputSchema: outputSchema,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: first-success descriptor: %w", ErrInvalidConfig, err)
	}
	return &FirstSuccess{descriptor: descriptor, maxCandidates: config.MaxCandidates, accept: config.Accept}, nil
}

func (f *FirstSuccess) Descriptor() agent.Descriptor {
	if f == nil {
		return agent.Descriptor{}
	}
	return f.descriptor
}

func (f *FirstSuccess) Start(input agent.Payload) (agent.Execution, error) {
	if !f.valid() {
		return nil, ErrInvalidConfig
	}
	if err := f.descriptor.ValidateInput(input); err != nil {
		return nil, err
	}
	candidates, err := input.Decode[[]agent.ChildSpec]()
	if err != nil {
		return nil, err
	}
	state := firstSuccessState{Phase: competitionReady, Candidates: candidates}
	if err := state.validate(f.maxCandidates); err != nil {
		return nil, fmt.Errorf("%w: %w", agent.ErrInvalidPayload, err)
	}
	return &firstSuccessExecution{definition: f, state: state}, nil
}

func (f *FirstSuccess) Restore(ctx context.Context, state agent.ExecutionState) (agent.Execution, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if !f.valid() {
		return nil, ErrInvalidConfig
	}
	decoded, err := state.Decode[firstSuccessState](firstSuccessStateKind)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidState, err)
	}
	if err := decoded.validate(f.maxCandidates); err != nil {
		return nil, err
	}
	return &firstSuccessExecution{definition: f, state: decoded}, nil
}

func (f *FirstSuccess) valid() bool {
	return f != nil && f.descriptor.Valid() && f.maxCandidates > 0 && f.accept != nil
}

type firstSuccessExecution struct {
	definition *FirstSuccess
	state      firstSuccessState
}

func (f *firstSuccessExecution) Step(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	if err := ctx.Err(); err != nil {
		return agent.Transition{}, err
	}
	switch f.state.Phase {
	case competitionReady:
		if len(signals) != 0 {
			return agent.Transition{}, fmt.Errorf("%w: competition accepts only child protocol Signals", ErrInvalidProtocol)
		}
		effects := make([]agent.Effect, 0, len(f.state.Candidates))
		for _, candidate := range f.state.Candidates {
			effect, err := agent.NewChildStartEffect(candidate)
			if err != nil {
				return agent.Transition{}, err
			}
			effects = append(effects, effect)
		}
		f.state.Phase = competitionAwaitingStarts
		return agent.Continue(0, effects...)
	case competitionAwaitingStarts:
		return f.acceptStarts(signals)
	case competitionAwaitingOpen:
		return f.acceptWaitOpen(signals)
	case competitionWaiting:
		return f.acceptOutcomes(ctx, signals)
	default:
		return agent.Transition{}, fmt.Errorf("%w: competition has no next Step", ErrInvalidProtocol)
	}
}

func (f *firstSuccessExecution) acceptStarts(signals []agent.Signal) (agent.Transition, error) {
	if len(signals) == 0 {
		return agent.Transition{}, fmt.Errorf("%w: child start results are missing", ErrInvalidProtocol)
	}
	count := min(len(signals), f.state.batch().PendingStarts())
	starts := make([]agent.ChildStartResult, count)
	for index := range starts {
		started, err := agent.ParseChildStartResult(signals[index])
		if err != nil {
			return agent.Transition{}, err
		}
		starts[index] = started
	}
	if _, err := f.state.batch().AcceptStarts(starts); err != nil {
		return agent.Transition{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	f.state.Starts = append(f.state.Starts, starts...)
	consumed := uint32(count)
	if len(f.state.Starts) != len(f.state.Candidates) {
		return agent.Continue(consumed)
	}
	return f.continueCompetition(consumed)
}

func (f *firstSuccessExecution) acceptWaitOpen(signals []agent.Signal) (agent.Transition, error) {
	if len(signals) == 0 {
		return agent.Transition{}, fmt.Errorf("%w: competition wait opening is missing", ErrInvalidProtocol)
	}
	opened, err := agent.ParseChildWaitOpened(signals[0])
	if err != nil {
		return agent.Transition{}, err
	}
	want, err := f.state.waitSpec()
	if err != nil {
		return agent.Transition{}, err
	}
	waitID, err := f.state.batch().AcceptOpening(opened, want.Key, want.Boundary, want.Condition)
	if err != nil {
		return agent.Transition{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	f.state.WaitID = &waitID
	f.state.Phase = competitionWaiting
	return agent.Wait(1, waitID)
}

func (f *firstSuccessExecution) acceptOutcomes(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	if len(signals) == 0 || f.state.WaitID == nil {
		return agent.Transition{}, fmt.Errorf("%w: competition wait satisfaction is missing", ErrInvalidProtocol)
	}
	satisfied, err := agent.ParseChildWaitSatisfied(signals[0])
	if err != nil {
		return agent.Transition{}, err
	}
	wait, err := f.state.waitSpec()
	if err != nil {
		return agent.Transition{}, err
	}
	if _, err := f.state.batch().Complete(satisfied, wait.Key, wait.Boundary, wait.Condition); err != nil {
		return agent.Transition{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	outcomes := satisfied.Outcomes()
	if err := f.state.recordOutcomes(outcomes); err != nil {
		return agent.Transition{}, err
	}
	for _, outcome := range outcomes {
		if err := ctx.Err(); err != nil {
			return agent.Transition{}, err
		}
		if outcome.Result().Status() != agent.StatusCompleted {
			continue
		}
		accepted, err := f.definition.accept(ctx, outcome)
		if err != nil {
			return agent.Transition{}, fmt.Errorf("coordination: evaluate candidate %s: %w", outcome.Key(), err)
		}
		if accepted {
			winner := outcome.Key()
			f.state.Winner = &winner
			break
		}
	}
	return f.continueCompetition(1)
}

func (f *firstSuccessExecution) continueCompetition(consumed uint32) (agent.Transition, error) {
	f.state.WaitID = nil
	if f.state.Winner != nil || len(f.state.remaining()) == 0 {
		f.state.Phase = competitionCompleted
		output, err := agent.EncodePayload(f.state.result())
		if err != nil {
			return agent.Transition{}, err
		}
		return agent.Complete(consumed, output)
	}
	spec, err := f.state.waitSpec()
	if err != nil {
		return agent.Transition{}, err
	}
	effect, err := agent.NewChildWaitEffect(spec)
	if err != nil {
		return agent.Transition{}, err
	}
	f.state.Phase = competitionAwaitingOpen
	return agent.Continue(consumed, effect)
}

func (f *firstSuccessExecution) Snapshot() (agent.ExecutionState, error) {
	return agent.EncodeExecutionState(firstSuccessStateKind, f.state)
}

var _ agent.Execution = (*firstSuccessExecution)(nil)

var _ agent.Definition = (*FirstSuccess)(nil)
