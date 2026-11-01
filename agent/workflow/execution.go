package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"

	"github.com/Tangerg/scope/agent/internal/strategy/childcall"
	"github.com/Tangerg/scope/agent/internal/strategy/stepfail"
	"github.com/Tangerg/scope/agent/run"
)

const (
	singleBindingIdentityKind = "single"
	singleChildWaitKey        = "workflow.child"
	fanoutChildrenWaitKey     = "workflow.children"
)

type execution struct {
	definition *Definition
	state      executionState
}

func (e *execution) Step(ctx context.Context, signals []run.Signal) (run.Transition, error) {
	if e == nil || !e.definition.valid() {
		return run.Transition{}, ErrInvalidExecutionState
	}
	if err := ctx.Err(); err != nil {
		return run.Transition{}, err
	}
	switch e.state.phase(uint32(len(e.definition.stages))) {
	case phaseReady:
		return e.advance(ctx, signals)
	case phaseChild:
		return e.advanceChild(ctx, signals)
	case phaseAwaitingFanoutStarts:
		return e.acceptFanoutStarts(signals)
	case phaseAwaitingFanoutWaitOpen:
		return e.acceptFanoutWaitOpen(signals)
	case phaseWaitingFanout:
		return e.acceptFanoutCompletion(ctx, signals)
	case phaseCompleted:
		return run.Transition{}, fmt.Errorf("%w: completed Execution cannot advance", ErrInvalidProtocol)
	default:
		return run.Transition{}, ErrInvalidExecutionState
	}
}

func (e *execution) Snapshot() (run.ExecutionState, error) {
	return e.state.snapshot()
}

func (e *execution) advance(ctx context.Context, signals []run.Signal) (run.Transition, error) {
	if len(signals) != 0 {
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
		return e.startSingleChild(0, stage.call)
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
		return e.startSingleChild(0, binding)
	case StageKindFork, StageKindMap:
		return e.startFanoutWindow(ctx, 0)
	case StageKindLoop:
		e.state.LoopIteration = 1
		return e.startSingleChild(0, stage.loop.binding)
	default:
		return run.Transition{}, fmt.Errorf("%w: Stage %q has an unknown kind %q", ErrInvalidExecutionState, stage.id, stage.kind)
	}
}

func (e *execution) startSingleChild(consumedSignals uint32, binding childBinding) (run.Transition, error) {
	input, err := run.ParsePayload(e.state.CurrentValue)
	if err != nil {
		return run.Transition{}, err
	}
	key, err := e.childKey()
	if err != nil {
		return run.Transition{}, err
	}
	effect, err := run.NewChildStartEffect(run.ChildSpec{
		Key: key, ProgramRef: binding.programRef(), Input: input,
		Budget: binding.budget, Capabilities: binding.capabilities,
	})
	if err != nil {
		return run.Transition{}, err
	}
	e.state.Child = &childcall.Single{}
	return run.Continue(consumedSignals, effect)
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

func (e *execution) advanceChild(ctx context.Context, signals []run.Signal) (run.Transition, error) {
	signal, err := e.state.Child.Window(signals)
	if err != nil {
		return run.Transition{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	waitKey, err := e.waitKey()
	if err != nil {
		return run.Transition{}, err
	}
	switch e.state.Child.Phase() {
	case childcall.PhaseAwaitingStart:
		return e.acceptChildStart(signal, waitKey)
	case childcall.PhaseAwaitingOpening:
		waitID, openErr := e.state.Child.AcceptOpening(signal)
		if openErr != nil {
			return run.Transition{}, fmt.Errorf("%w: Stage %q child wait opening: %w", ErrInvalidProtocol, e.stage().id, openErr)
		}
		return run.Wait(1, waitID)
	default:
		outcome, completeErr := e.state.Child.Complete(signal, waitKey, run.ChildWaitBoundaryDrained)
		if completeErr != nil {
			return run.Transition{}, fmt.Errorf("%w: Stage %q child completion: %w", ErrInvalidProtocol, e.stage().id, completeErr)
		}
		return e.acceptChildCompletion(ctx, outcome)
	}
}

func (e *execution) acceptChildStart(signal run.Signal, waitKey run.WaitKey) (run.Transition, error) {
	result, err := e.state.Child.AcceptStart(signal)
	if err != nil {
		return run.Transition{}, fmt.Errorf("%w: Stage %q child start: %w", ErrInvalidProtocol, e.stage().id, err)
	}
	if failure, failed := result.Failure(); failed {
		return run.Fail(1, failure)
	}
	effect, err := e.state.Child.WaitEffect(waitKey, run.ChildWaitBoundaryDrained)
	if err != nil {
		return run.Transition{}, err
	}
	return run.Continue(1, effect)
}

func (e *execution) acceptChildCompletion(ctx context.Context, outcome run.ChildOutcome) (run.Transition, error) {
	failure, output, err := childOutcome(outcome, e.stage().failureCode, "Child Process for Stage "+e.stageInvocationLabel())
	if err != nil {
		return run.Transition{}, err
	}
	if failure != nil {
		return run.Fail(1, *failure)
	}
	// The Runtime admitted this Output against the child's own Descriptor,
	// which the Stage contract equals.
	if e.stage().kind == StageKindLoop {
		return e.finishLoopIteration(ctx, 1, output)
	}
	e.state.CurrentValue = output.JSON()
	return e.finishStage(1)
}

func (e *execution) finishStage(consumedSignals uint32) (run.Transition, error) {
	e.state.finishStage()
	if e.state.StageIndex < uint32(len(e.definition.stages)) {
		return run.Continue(consumedSignals)
	}
	output, err := run.ParsePayload(e.state.CurrentValue)
	if err != nil {
		return run.Transition{}, err
	}
	e.state.CurrentValue = nil
	return run.Complete(consumedSignals, output)
}

func (e *execution) stage() Stage {
	return e.definition.stages[e.state.StageIndex]
}

// singleBindingIdentity names the current single-child binding: its Stage,
// selected case, and loop iteration, so successive bindings cannot share a child.
func (e *execution) singleBindingIdentity() []string {
	return []string{singleBindingIdentityKind, e.stage().id, e.state.SelectedCaseID, strconv.FormatUint(e.state.LoopIteration, 10)}
}

func (e *execution) childKey() (run.ChildKey, error) {
	return workflowChildKey(e.singleBindingIdentity()...)
}

// waitKey is the mailbox slot for a single-binding stage's child wait. A stage
// runs one bound child at a time and consumes its answer before the next, so a
// fixed key never collides; WaitID owns the wait's identity.
func (e *execution) waitKey() (run.WaitKey, error) {
	return run.ParseWaitKey(singleChildWaitKey)
}

func (e *execution) finishLoopIteration(
	ctx context.Context,
	consumedSignals uint32,
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
		return e.finishStage(consumedSignals)
	}
	e.state.clearSingleChild()
	if e.state.LoopIteration == math.MaxUint64 {
		return run.Transition{}, run.ErrCounterExhausted
	}
	e.state.LoopIteration++
	return e.startSingleChild(consumedSignals, stage.loop.binding)
}

func (e *execution) startFanoutWindow(ctx context.Context, consumedSignals uint32) (run.Transition, error) {
	stage := e.stage()
	start := e.state.fanoutWindowStart()
	inputs, count, err := stage.fanout.source.windowInputs(ctx, e.state.CurrentValue, start, stage.fanout.windowSize)
	if err != nil {
		if _, exceeded := errors.AsType[mapMaxItemsExceededError](err); exceeded {
			return stepfail.Transition(consumedSignals, run.FailureKindContract, stage.failureCode(failureSuffixMaxItemsExceeded),
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
		return e.finishStage(consumedSignals)
	}
	end := start + uint32(len(inputs))
	window := make([]fanoutChildState, end-start)
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
		effect, err := run.NewChildStartEffect(run.ChildSpec{
			Key: key, ProgramRef: member.binding.programRef(), Input: input,
			Budget: member.binding.budget, Capabilities: member.binding.capabilities,
		})
		if err != nil {
			return run.Transition{}, err
		}
		effects = append(effects, effect)
	}
	e.state.ActiveFanoutWindow = window
	return run.Continue(consumedSignals, effects...)
}

func (e *execution) fanoutBatch() (childcall.Batch, error) {
	return e.state.fanoutBatch(e.stage())
}

func (e *execution) acceptFanoutStarts(signals []run.Signal) (run.Transition, error) {
	batch, err := e.fanoutBatch()
	if err != nil {
		return run.Transition{}, err
	}
	count := batch.PendingStarts()
	if len(signals) < count {
		return run.Transition{}, fmt.Errorf("%w: fan-out starts require one settlement per member", ErrInvalidProtocol)
	}
	starts := make([]run.ChildStartResult, count)
	for index := range starts {
		starts[index], err = run.ParseChildStartResult(signals[index])
		if err != nil {
			return run.Transition{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
		}
	}
	indices, err := batch.AcceptStarts(starts)
	if err != nil {
		return run.Transition{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	consumed := uint32(count)
	window := slices.Clone(e.state.ActiveFanoutWindow)
	for index, offset := range indices {
		window[offset].recordStart(starts[index])
	}
	// A window with no started child fails at once; the Runtime owns that
	// Failure, so the state does not record the refusals that caused it.
	if !slices.ContainsFunc(window, fanoutChildState.started) {
		return run.Fail(consumed, firstFanoutFailure(window, nil))
	}
	e.state.ActiveFanoutWindow = window
	batch, err = e.fanoutBatch()
	if err != nil {
		return run.Transition{}, err
	}
	key, err := e.fanoutWaitKey()
	if err != nil {
		return run.Transition{}, err
	}
	spec, err := batch.WaitSpec(key, run.ChildWaitBoundaryDrained, run.AllChildren())
	if err != nil {
		return run.Transition{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	effect, err := run.NewChildWaitEffect(spec)
	if err != nil {
		return run.Transition{}, err
	}
	return run.Continue(consumed, effect)
}

func (e *execution) acceptFanoutWaitOpen(signals []run.Signal) (run.Transition, error) {
	if len(signals) == 0 {
		return run.Transition{}, ErrInvalidProtocol
	}
	opened, err := run.ParseChildWaitOpened(signals[0])
	if err != nil {
		return run.Transition{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	batch, err := e.fanoutBatch()
	if err != nil {
		return run.Transition{}, err
	}
	waitID, err := batch.AcceptOpening(opened)
	if err != nil {
		return run.Transition{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	e.state.FanoutWaitID = &waitID
	return run.Wait(1, waitID)
}

func (e *execution) acceptFanoutCompletion(ctx context.Context, signals []run.Signal) (run.Transition, error) {
	if len(signals) == 0 {
		return run.Transition{}, ErrInvalidProtocol
	}
	completed, err := run.ParseChildWaitSatisfied(signals[0])
	if err != nil {
		return run.Transition{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	batch, err := e.fanoutBatch()
	if err != nil {
		return run.Transition{}, err
	}
	key, err := e.fanoutWaitKey()
	if err != nil {
		return run.Transition{}, err
	}
	indices, err := batch.Complete(completed, key, run.ChildWaitBoundaryDrained, run.AllChildren())
	if err != nil {
		return run.Transition{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	outcomes := completed.Outcomes()
	windowOutputs := make([]json.RawMessage, len(e.state.ActiveFanoutWindow))
	outcomeFailures := make([]*run.Failure, len(e.state.ActiveFanoutWindow))
	for index, offset := range indices {
		failure, output, err := e.stage().fanoutOutcome(e.state.fanoutWindowStart()+uint32(offset), outcomes[index])
		if err != nil {
			return run.Transition{}, err
		}
		outcomeFailures[offset] = failure
		windowOutputs[offset] = output
	}
	// The Runtime owns the Failure that ends the Workflow, so the window keeps
	// only the start refusals its draining siblings needed.
	if failure := firstFanoutFailure(e.state.ActiveFanoutWindow, outcomeFailures); failure.Valid() {
		return run.Fail(1, failure)
	}
	e.state.CompletedFanoutOutputs = append(e.state.CompletedFanoutOutputs, windowOutputs...)
	e.state.FanoutWaitID = nil
	e.state.ActiveFanoutWindow = nil
	return e.startFanoutWindow(ctx, 1)
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

// fanoutWaitKey is the mailbox slot for a fanout stage's child wait. A stage
// holds one fanout window wait at a time and consumes it before opening the next,
// so a fixed key never collides; WaitID owns the wait's identity.
func (e *execution) fanoutWaitKey() (run.WaitKey, error) {
	return run.ParseWaitKey(fanoutChildrenWaitKey)
}

var _ run.Execution = (*execution)(nil)
