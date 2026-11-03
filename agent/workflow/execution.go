package workflow

import (
	"context"
	"encoding/json"
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
	definition *definition
	state      executionState
}

func (e *execution) Step(ctx context.Context, turn run.Turn) (run.Transition, error) {
	if e == nil || !e.definition.valid() {
		return run.Transition{}, ErrInvalidExecutionState
	}
	if err := ctx.Err(); err != nil {
		return run.Transition{}, err
	}
	switch e.state.phase(uint32(len(e.definition.stages))) {
	case phaseReady:
		return e.advance(ctx, turn)
	case phaseChild:
		return e.advanceChild(ctx, turn)
	case phaseFanout:
		return e.advanceFanout(ctx, turn)
	case phaseCompleted:
		return run.Transition{}, fmt.Errorf("%w: completed Execution cannot advance", ErrInvalidProtocol)
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
		return e.startSingleChild(turn, 0, stage.call)
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
		e.state.SelectedCaseID = selected
		return e.startSingleChild(turn, 0, binding)
	case StageKindFork, StageKindMap:
		return e.startFanoutWindow(ctx, turn, 0)
	case StageKindLoop:
		e.state.LoopIteration = 1
		return e.startSingleChild(turn, 0, stage.loop.binding)
	default:
		return run.Transition{}, fmt.Errorf("%w: Stage %q has an unknown kind %q", ErrInvalidExecutionState, stage.id, stage.kind)
	}
}

func (e *execution) startSingleChild(turn run.Turn, consumedMessages uint32, binding run.ChildBinding) (run.Transition, error) {
	input, err := run.ParsePayload(e.state.CurrentValue)
	if err != nil {
		return run.Transition{}, err
	}
	key, err := e.childKey()
	if err != nil {
		return run.Transition{}, err
	}
	spec, err := binding.Spec(key, input)
	if err != nil {
		return run.Transition{}, err
	}
	invocation, effect, err := child.Start(turn, 0, spec)
	if err != nil {
		return run.Transition{}, err
	}
	e.state.Child = &invocation
	return run.Yield(consumedMessages, []run.Effect{effect})
}

func (e *execution) stageInvocationLabel() string {
	stage := e.stage()
	switch stage.kind {
	case StageKindSwitch:
		return stage.id + ".case." + e.state.SelectedCaseID
	case StageKindLoop:
		return stage.id + ".iteration." + strconv.FormatUint(e.state.LoopIteration, 10)
	default:
		return stage.id
	}
}

func (e *execution) advanceChild(ctx context.Context, turn run.Turn) (run.Transition, error) {
	var consumed uint32
	for _, message := range turn.Messages {
		accepted, err := e.state.Child.Accept(message)
		if err != nil {
			return run.Transition{}, fmt.Errorf("%w: child completion: %w", ErrInvalidProtocol, err)
		}
		if !accepted {
			return run.Transition{}, ErrInvalidProtocol
		}
		consumed++
		if e.state.Child.Done() {
			break
		}
	}
	if e.state.Child.Rejection != nil {
		return run.Fail(consumed, *e.state.Child.Rejection)
	}
	if e.state.Child.Termination == nil {
		return run.Yield(consumed, nil)
	}
	failure, output, err := childOutcome(*e.state.Child.Termination, e.stage().failureCode, "Child Process for Stage "+e.stageInvocationLabel())
	if err != nil {
		return run.Transition{}, err
	}
	if failure != nil {
		return run.Fail(consumed, *failure)
	}
	if e.stage().kind == StageKindLoop {
		return e.finishLoopIteration(ctx, turn, consumed, output)
	}
	e.state.CurrentValue = output.JSON()
	return e.finishStage(consumed)
}

func (e *execution) finishStage(consumedMessages uint32) (run.Transition, error) {
	e.state.finishStage()
	if e.state.StageIndex < uint32(len(e.definition.stages)) {
		return run.Continue(consumedMessages)
	}
	output, err := run.ParsePayload(e.state.CurrentValue)
	if err != nil {
		return run.Transition{}, err
	}
	e.state.CurrentValue = nil
	return run.Complete(consumedMessages, output)
}

func (e *execution) stage() Stage {
	return e.definition.stages[e.state.StageIndex]
}

func (e *execution) childKey() (run.ChildKey, error) {
	return e.state.childKey(e.stage())
}

func (e *execution) finishLoopIteration(
	ctx context.Context,
	turn run.Turn,
	consumedMessages uint32,
	output run.Payload,
) (run.Transition, error) {
	stage := e.stage()
	satisfied, err := stage.loop.predicate(ctx, output.JSON())
	if err != nil {
		return run.Transition{}, err
	}
	e.state.CurrentValue = output.JSON()
	if satisfied || !stage.loop.maxIterations.Allows(e.state.LoopIteration, 1) {
		value, err := stage.loop.result(e.state.CurrentValue, e.state.LoopIteration, satisfied)
		if err != nil {
			return run.Transition{}, err
		}
		e.state.CurrentValue = value
		return e.finishStage(consumedMessages)
	}
	e.state.clearSingleChild()
	if e.state.LoopIteration == math.MaxUint64 {
		return run.Transition{}, run.ErrCounterExhausted
	}
	e.state.LoopIteration++
	return e.startSingleChild(turn, consumedMessages, stage.loop.binding)
}

func (e *execution) startFanoutWindow(ctx context.Context, turn run.Turn, consumedMessages uint32) (run.Transition, error) {
	stage := e.stage()
	start := e.state.fanoutWindowStart()
	inputs, count, err := stage.fanout.source.windowInputs(ctx, e.state.CurrentValue, start, stage.fanout.windowSize)
	if err != nil {
		if _, exceeded := errors.AsType[mapMaxItemsExceededError](err); exceeded {
			return stepfail.Transition(consumedMessages, run.FailureKindContract, stage.failureCode(failureSuffixMaxItemsExceeded),
				"Map Stage "+stage.id+" input exceeds its configured maximum items")
		}
		return run.Transition{}, err
	}
	if start == count {
		value, err := stage.fanout.complete(ctx, e.state.CurrentValue, e.state.CompletedFanoutOutputs)
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
		member, found := stage.fanout.source.member(index)
		if !found {
			return run.Transition{}, fmt.Errorf("%w: Stage %q has no fan-out member %d", ErrInvalidExecutionState, stage.id, index)
		}
		input := inputs[index-start]
		key, err := e.fanoutChildKey(index)
		if err != nil {
			return run.Transition{}, err
		}
		spec, err := member.binding.Spec(key, input)
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
	e.state.ActiveFanoutWindow = window
	return run.Yield(consumedMessages, effects)
}

func (e *execution) advanceFanout(ctx context.Context, turn run.Turn) (run.Transition, error) {
	for _, message := range turn.Messages {
		matched := false
		for index := range e.state.ActiveFanoutWindow {
			invocation := &e.state.ActiveFanoutWindow[index]
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
	for _, invocation := range e.state.ActiveFanoutWindow {
		if !invocation.Done() {
			return run.Yield(consumed, nil)
		}
	}
	outputs := make([]json.RawMessage, len(e.state.ActiveFanoutWindow))
	for index, invocation := range e.state.ActiveFanoutWindow {
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
	e.state.CompletedFanoutOutputs = append(e.state.CompletedFanoutOutputs, outputs...)
	e.state.ActiveFanoutWindow = nil
	return e.startFanoutWindow(ctx, turn, consumed)
}

func (e *execution) fanoutChildKey(index uint32) (run.ChildKey, error) {
	return fanoutChildKey(e.stage(), index)
}

func fanoutChildKey(stage Stage, index uint32) (run.ChildKey, error) {
	member, found := stage.fanout.source.member(index)
	if !found {
		return run.ChildKey{}, ErrInvalidExecutionState
	}
	return workflowChildKey(string(stage.kind), stage.id, member.id)
}

var _ run.Execution = (*execution)(nil)
