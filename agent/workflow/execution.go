package workflow

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/Tangerg/scope/agent/internal/strategy/child"
	"github.com/Tangerg/scope/agent/internal/strategy/stepfail"
	"github.com/Tangerg/scope/agent/run"
)

const (
	singleBindingIdentityKind = "single"
)

type execution struct {
	definition  *definition
	state       executionState
	fanoutInput fanoutInput
}

func (e *execution) Step(ctx context.Context, turn run.Turn) (run.Transition, error) {
	if err := ctx.Err(); err != nil {
		return run.Transition{}, err
	}
	switch e.state.phase() {
	case phaseReady:
		return e.advance(ctx, turn)
	case phaseChild:
		return e.advanceChild(ctx, turn)
	case phaseFanout:
		return e.advanceFanout(ctx, turn)
	default:
		return run.Transition{}, ErrInvalidExecutionState
	}
}

func (e *execution) Snapshot() (run.ExecutionState, error) {
	return e.state.snapshot()
}

func (e *execution) advance(ctx context.Context, turn run.Turn) (run.Transition, error) {
	if len(turn.Messages) != 0 {
		return run.Transition{}, fmt.Errorf("%w: Stage %q does not accept unsolicited Signals", ErrInvalidProtocol, e.stage().id)
	}
	stage := e.stage()
	switch stage.kind {
	case StageKindTransform:
		value, err := stage.transform(ctx, e.state.CurrentValue)
		if err != nil {
			return run.Transition{}, err
		}
		e.state.CurrentValue = value
		return e.finishStage(0)
	case StageKindCall:
		return e.startSingleChild(turn, 0, stage.call, "", 0)
	case StageKindSwitch:
		selected, err := stage.switcher.selectCase(ctx, e.state.CurrentValue)
		if err != nil {
			return run.Transition{}, err
		}
		binding, found := stage.switcher.binding(selected)
		if !found {
			return stepfail.Transition(
				0, run.FailureKindContract, stage.failureCode(failureSuffixCaseUnknown),
				"Switch Stage "+stage.id+" selected an undeclared case",
			)
		}
		return e.startSingleChild(turn, 0, binding, selected, 0)
	case StageKindFork, StageKindMap:
		return e.startFanoutWindow(ctx, turn, 0)
	case StageKindLoop:
		return e.startSingleChild(turn, 0, stage.loop.binding, "", 1)
	default:
		return run.Transition{}, fmt.Errorf("%w: Stage %q has an unknown kind %q", ErrInvalidExecutionState, stage.id, stage.kind)
	}
}

// startSingleChild runs the child a Call, the selected Switch case, or one
// Loop iteration names.
func (e *execution) startSingleChild(turn run.Turn, consumedMessages uint32, binding run.ChildBinding, caseID string, iteration uint64) (run.Transition, error) {
	key, err := singleChildKey(e.stage(), caseID, iteration)
	if err != nil {
		return run.Transition{}, err
	}
	spec, err := binding.Spec(key, e.state.CurrentValue)
	if err != nil {
		return run.Transition{}, err
	}
	invocation, effect, err := child.Start(turn, 0, spec)
	if err != nil {
		return run.Transition{}, err
	}
	e.state.Child = &singleChild{Case: caseID, Iteration: iteration, Invocation: invocation}
	return run.Yield(consumedMessages, []run.Effect{effect})
}

func (e *execution) stageInvocationLabel() string {
	stage := e.stage()
	switch stage.kind {
	case StageKindSwitch:
		return stage.id + ".case." + e.state.Child.Case
	case StageKindLoop:
		return stage.id + ".iteration." + strconv.FormatUint(e.state.Child.Iteration, 10)
	default:
		return stage.id
	}
}

func (e *execution) advanceChild(ctx context.Context, turn run.Turn) (run.Transition, error) {
	invocation := &e.state.Child.Invocation
	var consumed uint32
	for _, message := range turn.Messages {
		accepted, err := invocation.Accept(message)
		if err != nil {
			return run.Transition{}, err
		}
		if !accepted {
			return run.Transition{}, ErrInvalidProtocol
		}
		consumed++
		if invocation.Done() {
			break
		}
	}
	if invocation.Rejection != nil {
		return run.Fail(consumed, *invocation.Rejection)
	}
	if invocation.Termination == nil {
		return run.Yield(consumed, nil)
	}
	failure, output, err := childOutcome(*invocation.Termination, e.stage().failureCode, "Child Process for Stage "+e.stageInvocationLabel())
	if err != nil {
		return run.Transition{}, err
	}
	if failure != nil {
		return run.Fail(consumed, *failure)
	}
	if e.stage().kind == StageKindLoop {
		return e.finishLoopIteration(ctx, turn, consumed, output)
	}
	e.state.CurrentValue = output
	return e.finishStage(consumed)
}

func (e *execution) finishStage(consumedMessages uint32) (run.Transition, error) {
	e.fanoutInput = nil
	if uint64(e.state.StageIndex)+1 == uint64(len(e.definition.stages)) {
		return run.Complete(consumedMessages, e.state.CurrentValue)
	}
	e.state.finishStage()
	return run.Continue(consumedMessages)
}

func (e *execution) stage() Stage {
	return e.definition.stages[e.state.StageIndex]
}

func (e *execution) finishLoopIteration(
	ctx context.Context,
	turn run.Turn,
	consumedMessages uint32,
	output run.Payload,
) (run.Transition, error) {
	stage := e.stage()
	iteration := e.state.Child.Iteration
	satisfied, err := stage.loop.predicate(ctx, output)
	if err != nil {
		return run.Transition{}, err
	}
	e.state.CurrentValue = output
	if satisfied || !stage.loop.maxIterations.Allows(iteration, 1) {
		value, err := stage.loop.result(e.state.CurrentValue, iteration, satisfied)
		if err != nil {
			return run.Transition{}, err
		}
		e.state.CurrentValue = value
		return e.finishStage(consumedMessages)
	}
	if iteration == math.MaxUint64 {
		return run.Transition{}, run.ErrCounterExhausted
	}
	return e.startSingleChild(turn, consumedMessages, stage.loop.binding, "", iteration+1)
}

func (e *execution) startFanoutWindow(ctx context.Context, turn run.Turn, consumedMessages uint32) (run.Transition, error) {
	stage := e.stage()
	start := e.state.fanoutWindowStart()
	if e.fanoutInput == nil {
		input, err := stage.fanout.source.prepareInput(ctx, e.state.CurrentValue)
		if err != nil {
			if _, exceeded := errors.AsType[mapMaxItemsExceededError](err); exceeded {
				return stepfail.Transition(consumedMessages, run.FailureKindContract, stage.failureCode(failureSuffixMaxItemsExceeded),
					"Map Stage "+stage.id+" input exceeds its configured maximum items")
			}
			return run.Transition{}, err
		}
		e.fanoutInput = input
	}
	inputs, err := e.fanoutInput.windowInputs(ctx, start, stage.fanout.windowSize)
	if err != nil {
		return run.Transition{}, err
	}
	var completed []run.Payload
	if e.state.Fanout != nil {
		completed = e.state.Fanout.Completed
	}
	count := e.fanoutInput.count()
	if start == count {
		value, err := stage.fanout.complete(ctx, e.state.CurrentValue, completed)
		if err != nil {
			return run.Transition{}, err
		}
		e.state.CurrentValue = value
		return e.finishStage(consumedMessages)
	}
	end := start + uint32(len(inputs))
	window := make([]child.Invocation, end-start)
	effects := make([]run.Effect, 0, end-start)
	for index := start; index < end; index++ {
		if err := ctx.Err(); err != nil {
			return run.Transition{}, err
		}
		member, key, err := stage.fanoutChild(index)
		if err != nil {
			return run.Transition{}, err
		}
		spec, err := member.binding.Spec(key, inputs[index-start])
		if err != nil {
			return run.Transition{}, err
		}
		invocation, effect, err := child.Start(turn, uint32(len(effects)), spec)
		if err != nil {
			return run.Transition{}, err
		}
		window[index-start] = invocation
		effects = append(effects, effect)
	}
	e.state.Fanout = &fanoutProgress{Window: window, Completed: completed}
	return run.Yield(consumedMessages, effects)
}

func (e *execution) advanceFanout(ctx context.Context, turn run.Turn) (run.Transition, error) {
	for _, message := range turn.Messages {
		matched := false
		for index := range e.state.Fanout.Window {
			invocation := &e.state.Fanout.Window[index]
			if invocation.Done() {
				continue
			}
			accepted, err := invocation.Accept(message)
			if err != nil {
				return run.Transition{}, err
			}
			if accepted {
				matched = true
				break
			}
		}
		if !matched {
			return run.Transition{}, ErrInvalidProtocol
		}
	}
	consumed := uint32(len(turn.Messages))
	window := e.state.Fanout.Window
	for _, invocation := range window {
		if !invocation.Done() {
			return run.Yield(consumed, nil)
		}
	}
	outputs := make([]run.Payload, len(window))
	for index, invocation := range window {
		if invocation.Rejection != nil {
			return run.Fail(consumed, *invocation.Rejection)
		}
		failure, output, err := e.stage().fanoutOutcome(e.state.fanoutWindowStart()+uint32(index), *invocation.Termination)
		if err != nil {
			return run.Transition{}, err
		}
		if failure != nil {
			return run.Fail(consumed, *failure)
		}
		outputs[index] = output
	}
	// The finished window leaves only its outputs until the next one starts in
	// this same Step, so no snapshot sees the state between them.
	e.state.Fanout = &fanoutProgress{Completed: append(e.state.Fanout.Completed, outputs...)}
	return e.startFanoutWindow(ctx, turn, consumed)
}

var _ run.Execution = (*execution)(nil)
