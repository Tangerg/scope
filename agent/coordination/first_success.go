package coordination

import (
	"context"
	"fmt"

	"github.com/Tangerg/scope/agent/internal/strategy/restore"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

const firstSuccessStateKind = "coordination.first_success"

// SuccessPredicate decides whether a completed child satisfies the business
// goal. It runs inside Step and must be bounded, deterministic, side-effect-free,
// and honor ctx cancellation. Non-completed children never satisfy success.
type SuccessPredicate func(ctx context.Context, candidate run.ChildKey, outcome run.ChildOutcome) (bool, error)

// FirstSuccessConfig binds a finite candidate bound and pure decision policy.
// The Program configuration digest must identify both values.
type FirstSuccessConfig struct {
	Name          string
	Description   string
	MaxCandidates uint32
	Accept        SuccessPredicate
}

// FirstSuccess accepts a non-empty []run.ChildSpec and runs those candidates
// under one ownership scope. It retains failed starts and observed terminal
// results, chooses the first accepted result in each request-ordered wait
// response, and completes with FirstSuccessResult. An empty Winner means every
// candidate failed to satisfy Accept; it is an explicit business result.
// Candidates arrive as runtime input, so FirstSuccess binds no children of its
// own: each candidate Program must be available through the Runtime's
// ProgramResolver.
//
// Every candidate's admission and initialization must settle before results are
// considered. A slow later admission can therefore delay acceptance of an
// already completed child. When multiple results are visible together, request
// order determines the winner, independently of their completion timestamps.
//
// Results are observed at the terminal-result boundary. Completion starts
// cancellation of remaining descendants; it does not establish their drain.
type FirstSuccess struct {
	descriptor    run.Descriptor
	maxCandidates uint32
	accept        SuccessPredicate
}

func NewFirstSuccess(config FirstSuccessConfig) (*FirstSuccess, error) {
	if config.MaxCandidates == 0 || config.Accept == nil {
		return nil, fmt.Errorf("%w: candidate bound and success predicate are required", ErrInvalidConfig)
	}
	inputSchema, err := jsonschema.For[[]run.ChildSpec]()
	if err != nil {
		return nil, err
	}
	outputSchema, err := jsonschema.For[FirstSuccessResult]()
	if err != nil {
		return nil, err
	}
	descriptor, err := run.NewDescriptor(run.DescriptorConfig{
		Name: config.Name, Description: config.Description, InputSchema: inputSchema, OutputSchema: outputSchema,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: first-success descriptor: %w", ErrInvalidConfig, err)
	}
	return &FirstSuccess{descriptor: descriptor, maxCandidates: config.MaxCandidates, accept: config.Accept}, nil
}

func (*FirstSuccess) ChildPrograms() []run.Program[run.Payload, run.Payload] { return nil }

func (f *FirstSuccess) Descriptor() run.Descriptor {
	if f == nil {
		return run.Descriptor{}
	}
	return f.descriptor
}

func (f *FirstSuccess) Start(input run.Payload) (run.Execution, error) {
	if !f.valid() {
		return nil, ErrInvalidConfig
	}
	if err := f.descriptor.ValidateInput(input); err != nil {
		return nil, err
	}
	candidates, err := input.Decode[[]run.ChildSpec]()
	if err != nil {
		return nil, err
	}
	state := firstSuccessState{Candidates: candidates}
	if err := state.validate(context.Background(), f.maxCandidates); err != nil {
		return nil, fmt.Errorf("%w: %w", run.ErrInvalidPayload, err)
	}
	return &firstSuccessExecution{definition: f, state: state}, nil
}

func (f *FirstSuccess) Restore(ctx context.Context, state run.ExecutionState) (run.Execution, error) {
	if !f.valid() {
		return nil, ErrInvalidConfig
	}
	decoded, err := restore.Decode(ctx, state, firstSuccessStateKind, ErrInvalidExecutionState, func(ctx context.Context, decoded firstSuccessState) error {
		return decoded.validate(ctx, f.maxCandidates)
	})
	if err != nil {
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

func (f *firstSuccessExecution) Step(ctx context.Context, signals []run.Signal) (run.Transition, error) {
	if err := ctx.Err(); err != nil {
		return run.Transition{}, err
	}
	switch f.state.phase() {
	case competitionReady:
		if len(signals) != 0 {
			return run.Transition{}, fmt.Errorf("%w: competition accepts only child protocol Signals", ErrInvalidProtocol)
		}
		effects := make([]run.Effect, 0, len(f.state.Candidates))
		for _, candidate := range f.state.Candidates {
			effect, err := run.NewChildStartEffect(candidate)
			if err != nil {
				return run.Transition{}, err
			}
			effects = append(effects, effect)
		}
		f.state.Results = make([]*CandidateResult, len(f.state.Candidates))
		return run.Continue(0, effects...)
	case competitionAwaitingStarts:
		return f.acceptStarts(signals)
	case competitionAwaitingOpen:
		return f.acceptWaitOpen(signals)
	case competitionWaiting:
		return f.acceptOutcomes(ctx, signals)
	default:
		return run.Transition{}, fmt.Errorf("%w: competition has no next Step", ErrInvalidProtocol)
	}
}

func (f *firstSuccessExecution) acceptStarts(signals []run.Signal) (run.Transition, error) {
	if len(signals) == 0 {
		return run.Transition{}, fmt.Errorf("%w: child start results are missing", ErrInvalidProtocol)
	}
	count := min(len(signals), f.state.batch().PendingStarts())
	starts := make([]run.ChildStartResult, count)
	for index := range starts {
		started, err := signals[index].Frame[run.ChildStartResult]()
		if err != nil {
			return run.Transition{}, fmt.Errorf("%w: child start result: %w", ErrInvalidProtocol, err)
		}
		starts[index] = started
	}
	indices, err := f.state.batch().AcceptStarts(starts)
	if err != nil {
		return run.Transition{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	for offset, index := range indices {
		f.state.Results[index] = &CandidateResult{Start: &starts[offset]}
	}
	consumed := uint32(count)
	if f.state.admitted() != len(f.state.Candidates) {
		return run.Continue(consumed)
	}
	return f.continueCompetition(consumed, nil)
}

func (f *firstSuccessExecution) acceptWaitOpen(signals []run.Signal) (run.Transition, error) {
	if len(signals) == 0 {
		return run.Transition{}, fmt.Errorf("%w: competition wait opening is missing", ErrInvalidProtocol)
	}
	opened, err := signals[0].Frame[run.ChildWaitOpened]()
	if err != nil {
		return run.Transition{}, fmt.Errorf("%w: child wait opening: %w", ErrInvalidProtocol, err)
	}
	waitID, err := f.state.batch().AcceptOpening(opened)
	if err != nil {
		return run.Transition{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	f.state.WaitID = &waitID
	return run.Wait(1, waitID)
}

func (f *firstSuccessExecution) acceptOutcomes(ctx context.Context, signals []run.Signal) (run.Transition, error) {
	if len(signals) == 0 || f.state.WaitID == nil {
		return run.Transition{}, fmt.Errorf("%w: competition wait satisfaction is missing", ErrInvalidProtocol)
	}
	satisfied, err := signals[0].Frame[run.ChildWaitSatisfied]()
	if err != nil {
		return run.Transition{}, fmt.Errorf("%w: child wait satisfaction: %w", ErrInvalidProtocol, err)
	}
	wait, err := f.state.waitSpec()
	if err != nil {
		return run.Transition{}, err
	}
	indices, err := f.state.batch().Complete(satisfied, wait.Key, wait.Boundary, wait.Condition)
	if err != nil {
		return run.Transition{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	outcomes := satisfied.Outcomes()
	f.state.recordOutcomes(indices, outcomes)
	var winner *uint32
	for offset, outcome := range outcomes {
		if err := ctx.Err(); err != nil {
			return run.Transition{}, err
		}
		if outcome.Result().Termination().Status() != run.StatusCompleted {
			continue
		}
		candidate := f.state.Candidates[indices[offset]].Key
		accepted, err := f.definition.accept(ctx, candidate, outcome)
		if err != nil {
			return run.Transition{}, fmt.Errorf("coordination: evaluate candidate %s: %w", candidate, err)
		}
		if accepted {
			winner = new(uint32(indices[offset]))
			break
		}
	}
	return f.continueCompetition(1, winner)
}

func (f *firstSuccessExecution) continueCompetition(consumed uint32, winner *uint32) (run.Transition, error) {
	f.state.WaitID = nil
	if winner != nil || !f.state.anyRemaining() {
		output, err := run.EncodePayload(f.state.result(winner))
		if err != nil {
			return run.Transition{}, err
		}
		f.state = firstSuccessState{Candidates: f.state.Candidates, Completed: true}
		return run.Complete(consumed, output)
	}
	spec, err := f.state.waitSpec()
	if err != nil {
		return run.Transition{}, err
	}
	effect, err := run.NewChildWaitEffect(spec)
	if err != nil {
		return run.Transition{}, err
	}
	return run.Continue(consumed, effect)
}

func (f *firstSuccessExecution) Snapshot() (run.ExecutionState, error) {
	return run.EncodeExecutionState(firstSuccessStateKind, f.state)
}

var _ run.Execution = (*firstSuccessExecution)(nil)

var _ run.Definition = (*FirstSuccess)(nil)
