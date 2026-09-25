package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/internal/childcall"
)

type execution struct {
	definition *Definition
	state      executionState
}

func (e *execution) Step(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	transition, err := e.step(ctx, signals)
	if err != nil {
		return agent.Transition{}, agent.ClassifyStepError(err)
	}
	return transition, nil
}

func (e *execution) step(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	if e == nil || !e.definition.valid() {
		return agent.Transition{}, ErrInvalidExecutionState
	}
	if err := ctx.Err(); err != nil {
		return agent.Transition{}, err
	}
	switch e.state.Phase {
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
		return agent.Transition{}, fmt.Errorf("%w: completed Execution cannot advance", ErrInvalidProtocol)
	default:
		return agent.Transition{}, ErrInvalidExecutionState
	}
}

func (e *execution) Snapshot() (agent.ExecutionState, error) {
	return e.state.snapshot()
}

func (e *execution) advance(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	if len(signals) != 0 {
		return agent.Transition{}, fmt.Errorf("%w: Stage %q does not accept unsolicited Signals", ErrInvalidProtocol, e.stage().id)
	}
	stage := e.stage()
	switch stage.kind {
	case StageKindTransform:
		value, err := stage.transform(ctx, e.state.CurrentValue)
		if err != nil {
			return agent.Transition{}, err
		}
		e.state.CurrentValue = value
		return e.finishStage(0)
	case StageKindCall:
		return e.startSingleChild(0, stage.call)
	case StageKindSwitch:
		selected, err := stage.switcher.selectCase(ctx, e.state.CurrentValue)
		if err != nil {
			if _, ok := errors.AsType[unknownSwitchCaseError](err); ok {
				return e.failContract(
					0, stage.failureCode(failureSuffixCaseUnknown),
					"Switch Stage "+stage.id+" selected an undeclared case",
				)
			}
			return agent.Transition{}, err
		}
		binding, found := stage.switcher.binding(selected)
		if !found {
			return agent.Transition{}, fmt.Errorf("%w: Switch Stage %q selected case %q has no binding", ErrInvalidExecutionState, stage.id, selected)
		}
		e.state.SelectedCaseID = selected
		return e.startSingleChild(0, binding)
	case StageKindFork, StageKindMap:
		return e.startFanoutWindow(ctx, 0)
	case StageKindLoop:
		e.state.LoopIteration = 1
		return e.startSingleChild(0, stage.loop.binding)
	default:
		return agent.Transition{}, fmt.Errorf("%w: Stage %q has an unknown kind %q", ErrInvalidExecutionState, stage.id, stage.kind)
	}
}

func (e *execution) startSingleChild(consumedSignals uint32, binding childBinding) (agent.Transition, error) {
	input, err := agent.ParsePayload(e.state.CurrentValue)
	if err != nil {
		return agent.Transition{}, err
	}
	key, err := e.childKey()
	if err != nil {
		return agent.Transition{}, err
	}
	effect, err := agent.NewChildStartEffect(agent.ChildSpec{
		Key: key, DeploymentRef: binding.deploymentRef, Input: input,
		Budget: binding.budget, Capabilities: binding.capabilities,
	})
	if err != nil {
		return agent.Transition{}, err
	}
	e.state.Child = &childcall.Single{}
	e.state.Phase = phaseChild
	return agent.Continue(consumedSignals, effect)
}

func (e *execution) singleChildBinding() (childBinding, bool) {
	stage := e.stage()
	switch stage.kind {
	case StageKindCall:
		return stage.call, true
	case StageKindSwitch:
		return stage.switcher.binding(e.state.SelectedCaseID)
	case StageKindLoop:
		return stage.loop.binding, true
	default:
		return childBinding{}, false
	}
}

func (e *execution) clearSingleChild() {
	e.state.SelectedCaseID = ""
	e.state.Child = nil
}

func (e *execution) stageInvocationLabel() string {
	if e.stage().kind == StageKindSwitch {
		return e.stage().id + ".case." + e.state.SelectedCaseID
	}
	if e.stage().kind == StageKindLoop {
		return e.stage().id + ".iteration." + strconv.FormatUint(e.state.LoopIteration, 10)
	}
	return e.stage().id
}

func (e *execution) advanceChild(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	signal, err := e.state.Child.Window(signals)
	if err != nil {
		return agent.Transition{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	key, err := e.childKey()
	if err != nil {
		return agent.Transition{}, err
	}
	waitKey, err := e.waitKey()
	if err != nil {
		return agent.Transition{}, err
	}
	switch e.state.Child.Phase() {
	case childcall.AwaitingStart:
		return e.acceptChildStart(signal, key, waitKey)
	case childcall.AwaitingOpening:
		waitID, openErr := e.state.Child.AcceptOpening(signal, waitKey, agent.ChildWaitBoundaryDrained)
		if openErr != nil {
			return agent.Transition{}, fmt.Errorf("%w: Stage %q child wait opening: %w", ErrInvalidProtocol, e.stage().id, openErr)
		}
		return agent.Wait(1, waitID)
	default:
		outcome, completeErr := e.state.Child.Complete(signal, key, waitKey, agent.ChildWaitBoundaryDrained)
		if completeErr != nil {
			return agent.Transition{}, fmt.Errorf("%w: Stage %q child completion: %w", ErrInvalidProtocol, e.stage().id, completeErr)
		}
		return e.acceptChildCompletion(ctx, outcome)
	}
}

func (e *execution) acceptChildStart(signal agent.Signal, key agent.ChildKey, waitKey agent.WaitKey) (agent.Transition, error) {
	binding, bound := e.singleChildBinding()
	if !bound {
		return agent.Transition{}, ErrInvalidExecutionState
	}
	result, err := e.state.Child.AcceptStart(signal, key, binding.deploymentRef)
	if err != nil {
		return agent.Transition{}, fmt.Errorf("%w: Stage %q child start: %w", ErrInvalidProtocol, e.stage().id, err)
	}
	if failure, failed := result.Failure(); failed {
		return agent.Fail(1, failure)
	}
	effect, err := e.state.Child.WaitEffect(waitKey, agent.ChildWaitBoundaryDrained)
	if err != nil {
		return agent.Transition{}, err
	}
	return agent.Continue(1, effect)
}

func (e *execution) acceptChildCompletion(ctx context.Context, outcome agent.ChildOutcome) (agent.Transition, error) {
	if unresolved, known := outcome.SubtreeUnresolvedEffects(); !known || len(unresolved) != 0 {
		return e.fail(1, e.stage().failureCode(failureSuffixUnresolvedEffects), "Child subtree has unresolved Effects", agent.FailureKindExternal)
	}
	result := outcome.Result()
	if result.Status() != agent.StatusCompleted {
		if failure, failed := result.Termination().Failure(); failed {
			return agent.Fail(1, failure)
		}
		return e.fail(
			1,
			e.stage().failureCode(failureSuffixChildNotCompleted),
			"Child Process for Stage "+e.stageInvocationLabel()+" terminated with status "+result.Status().String(),
			agent.FailureKindExternal,
		)
	}
	output, present := result.Output()
	if !present {
		return e.failContract(1, e.stage().failureCode(failureSuffixOutputMissing), "Completed child Process returned no Output")
	}
	if err := e.singleChildOutputSchema().Validate(output.JSON()); err != nil {
		return e.failContract(1, e.stage().failureCode(failureSuffixOutputInvalid), "Child Process Output violated the Stage contract")
	}
	if e.stage().kind == StageKindLoop {
		return e.finishLoopIteration(ctx, 1, output)
	}
	e.state.CurrentValue = output.JSON()
	e.clearSingleChild()
	e.state.Phase = phaseReady
	return e.finishStage(1)
}

func (e *execution) finishStage(consumedSignals uint32) (agent.Transition, error) {
	e.clearSingleChild()
	e.clearFanout()
	e.state.LoopIteration = 0
	e.state.StageIndex++
	if e.state.StageIndex < uint32(len(e.definition.stages)) {
		e.state.Phase = phaseReady
		return agent.Continue(consumedSignals)
	}
	output, err := agent.ParsePayload(e.state.CurrentValue)
	if err != nil {
		return agent.Transition{}, err
	}
	e.state.Phase = phaseCompleted
	return agent.Complete(consumedSignals, output)
}

func (e *execution) failContract(consumedSignals uint32, code, message string) (agent.Transition, error) {
	return e.fail(consumedSignals, code, message, agent.FailureKindContract)
}

func (*execution) fail(
	consumedSignals uint32,
	code string,
	message string,
	kind agent.FailureKind,
) (agent.Transition, error) {
	failure, err := agent.NewFailure(kind, code, message)
	if err != nil {
		return agent.Transition{}, err
	}
	return agent.Fail(consumedSignals, failure)
}

func (e *execution) stage() Stage {
	return e.definition.stages[e.state.StageIndex]
}

func (e *execution) childKey() (agent.ChildKey, error) {
	return workflowChildKey(
		"single", e.stage().id, e.state.SelectedCaseID,
		strconv.FormatUint(e.state.LoopIteration, 10),
	)
}

func (e *execution) waitKey() (agent.WaitKey, error) {
	return workflowWaitKey(
		"single", e.stage().id, e.state.SelectedCaseID,
		strconv.FormatUint(e.state.LoopIteration, 10),
	)
}

func (e *execution) singleChildOutputSchema() agent.Schema {
	if e.stage().kind == StageKindLoop {
		return e.stage().loop.valueSchema
	}
	return e.stage().outputSchema
}

func (e *execution) finishLoopIteration(
	ctx context.Context,
	consumedSignals uint32,
	output agent.Payload,
) (agent.Transition, error) {
	stage := e.stage()
	satisfied, err := stage.loop.predicate(ctx, output.JSON())
	if err != nil {
		return agent.Transition{}, err
	}
	e.state.CurrentValue = output.JSON()
	if satisfied || !stage.loop.maxIterations.Allows(e.state.LoopIteration, 1) {
		value, err := stage.loop.result(e.state.CurrentValue, e.state.LoopIteration, satisfied)
		if err != nil {
			return agent.Transition{}, err
		}
		e.state.CurrentValue = value
		e.clearSingleChild()
		e.state.Phase = phaseReady
		return e.finishStage(consumedSignals)
	}
	e.clearSingleChild()
	if e.state.LoopIteration == ^uint64(0) {
		return agent.Transition{}, agent.ErrCounterExhausted
	}
	e.state.LoopIteration++
	return e.startSingleChild(consumedSignals, stage.loop.binding)
}

func (e *execution) startFanoutWindow(ctx context.Context, consumedSignals uint32) (agent.Transition, error) {
	stage := e.stage()
	start := e.state.fanoutWindowStart()
	inputs, count, err := stage.fanout.source.windowInputs(ctx, e.state.CurrentValue, start, stage.fanout.windowSize)
	if err != nil {
		if _, exceeded := errors.AsType[mapMaxItemsExceededError](err); exceeded {
			return e.failContract(consumedSignals, stage.failureCode(failureSuffixMaxItemsExceeded),
				"Map Stage "+stage.id+" input exceeds its configured maximum items")
		}
		return agent.Transition{}, err
	}
	if uint32(len(inputs)) != min(stage.fanout.windowSize, count-start) {
		return agent.Transition{}, ErrInvalidExecutionState
	}
	if start == count {
		value, err := stage.fanout.complete(ctx, e.state.CompletedFanoutOutputs)
		if err != nil {
			return agent.Transition{}, err
		}
		e.state.CurrentValue = value
		e.state.Phase = phaseReady
		return e.finishStage(consumedSignals)
	}
	end := start + uint32(len(inputs))
	window := make([]fanoutChildState, end-start)
	effects := make([]agent.Effect, 0, end-start)
	for index := start; index < end; index++ {
		if err := ctx.Err(); err != nil {
			return agent.Transition{}, err
		}
		member, found := stage.fanout.source.member(index)
		if !found {
			return agent.Transition{}, fmt.Errorf("%w: Stage %q has no fan-out member %d", ErrInvalidExecutionState, stage.id, index)
		}
		input := inputs[index-start]
		key, err := e.fanoutChildKey(index)
		if err != nil {
			return agent.Transition{}, err
		}
		effect, err := agent.NewChildStartEffect(agent.ChildSpec{
			Key: key, DeploymentRef: member.binding.deploymentRef, Input: input,
			Budget: member.binding.budget, Capabilities: member.binding.capabilities,
		})
		if err != nil {
			return agent.Transition{}, err
		}
		effects = append(effects, effect)
	}
	e.state.ActiveFanoutWindow = window
	e.state.Phase = phaseAwaitingFanoutStarts
	return agent.Continue(consumedSignals, effects...)
}

func (e *execution) fanoutBatch() (childcall.Batch, error) {
	batch := childcall.Batch{Children: make([]childcall.Child, len(e.state.ActiveFanoutWindow))}
	if e.state.FanoutWaitID != nil {
		batch.WaitID = *e.state.FanoutWaitID
	}
	for offset, progress := range e.state.ActiveFanoutWindow {
		index := e.state.fanoutWindowStart() + uint32(offset)
		member, found := e.stage().fanout.source.member(index)
		if !found {
			return childcall.Batch{}, fmt.Errorf("%w: Stage %q has no fan-out member %d", ErrInvalidExecutionState, e.stage().id, index)
		}
		key, err := e.fanoutChildKey(index)
		if err != nil {
			return childcall.Batch{}, err
		}
		child := &batch.Children[offset]
		child.Key, child.Deployment = key, member.binding.deploymentRef
		if progress.ChildProcessID != nil {
			child.ProcessID = *progress.ChildProcessID
		}
		child.Done = progress.ChildProcessID == nil && progress.Failure != nil
	}
	return batch, nil
}

func (e *execution) acceptFanoutStarts(signals []agent.Signal) (agent.Transition, error) {
	batch, err := e.fanoutBatch()
	if err != nil {
		return agent.Transition{}, err
	}
	count := batch.PendingStarts()
	if len(signals) < count {
		return agent.Transition{}, fmt.Errorf("%w: fan-out starts require one settlement per member", ErrInvalidProtocol)
	}
	starts := make([]agent.ChildStartResult, count)
	for index := range starts {
		starts[index], err = agent.ParseChildStartResult(signals[index])
		if err != nil {
			return agent.Transition{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
		}
	}
	indices, err := batch.AcceptStarts(starts)
	if err != nil {
		return agent.Transition{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	for index, offset := range indices {
		if failure, failed := starts[index].Failure(); failed {
			e.state.ActiveFanoutWindow[offset].Failure = &failure
		} else if id, started := starts[index].ProcessID(); started {
			e.state.ActiveFanoutWindow[offset].ChildProcessID = &id
		}
	}
	consumed := uint32(count)
	if !e.fanoutHasStartedChildren() {
		return agent.Fail(consumed, e.firstFanoutFailure())
	}
	batch, err = e.fanoutBatch()
	if err != nil {
		return agent.Transition{}, err
	}
	key, err := e.fanoutWaitKey()
	if err != nil {
		return agent.Transition{}, err
	}
	spec, err := batch.WaitSpec(key, agent.ChildWaitBoundaryDrained, agent.AllChildren())
	if err != nil {
		return agent.Transition{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	effect, err := agent.NewChildWaitEffect(spec)
	if err != nil {
		return agent.Transition{}, err
	}
	e.state.Phase = phaseAwaitingFanoutWaitOpen
	return agent.Continue(consumed, effect)
}

func (e *execution) acceptFanoutWaitOpen(signals []agent.Signal) (agent.Transition, error) {
	if len(signals) == 0 {
		return agent.Transition{}, ErrInvalidProtocol
	}
	opened, err := agent.ParseChildWaitOpened(signals[0])
	if err != nil {
		return agent.Transition{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	batch, err := e.fanoutBatch()
	if err != nil {
		return agent.Transition{}, err
	}
	key, err := e.fanoutWaitKey()
	if err != nil {
		return agent.Transition{}, err
	}
	waitID, err := batch.AcceptOpening(opened, key, agent.ChildWaitBoundaryDrained, agent.AllChildren())
	if err != nil {
		return agent.Transition{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	e.state.FanoutWaitID = &waitID
	e.state.Phase = phaseWaitingFanout
	return agent.Wait(1, waitID)
}

func (e *execution) acceptFanoutCompletion(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	if len(signals) == 0 {
		return agent.Transition{}, ErrInvalidProtocol
	}
	completed, err := agent.ParseChildWaitSatisfied(signals[0])
	if err != nil {
		return agent.Transition{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	batch, err := e.fanoutBatch()
	if err != nil {
		return agent.Transition{}, err
	}
	key, err := e.fanoutWaitKey()
	if err != nil {
		return agent.Transition{}, err
	}
	indices, err := batch.Complete(completed, key, agent.ChildWaitBoundaryDrained, agent.AllChildren())
	if err != nil {
		return agent.Transition{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	outcomes := completed.Outcomes()
	windowOutputs := make([]json.RawMessage, len(e.state.ActiveFanoutWindow))
	for index, offset := range indices {
		failure, output, err := e.fanoutOutcome(e.state.fanoutWindowStart()+uint32(offset), outcomes[index])
		if err != nil {
			return agent.Transition{}, err
		}
		if failure != nil {
			e.state.ActiveFanoutWindow[offset].Failure = failure
		} else {
			windowOutputs[offset] = output
		}
	}
	if failure := e.firstFanoutFailure(); failure.Valid() {
		return agent.Fail(1, failure)
	}
	e.state.CompletedFanoutOutputs = append(e.state.CompletedFanoutOutputs, windowOutputs...)
	e.state.FanoutWaitID = nil
	e.state.ActiveFanoutWindow = nil
	return e.startFanoutWindow(ctx, 1)
}

func (e *execution) fanoutOutcome(
	index uint32,
	outcome agent.ChildOutcome,
) (*agent.Failure, json.RawMessage, error) {
	if unresolved, known := outcome.SubtreeUnresolvedEffects(); !known || len(unresolved) != 0 {
		failure, err := agent.NewFailure(agent.FailureKindExternal, e.stage().fanoutFailureCode(failureSuffixUnresolvedEffects), e.fanoutFailureMessage(index, "has unresolved subtree Effects"))
		return &failure, nil, err
	}
	result := outcome.Result()
	if result.Status() != agent.StatusCompleted {
		if failure, failed := result.Termination().Failure(); failed {
			return &failure, nil, nil
		}
		code := e.stage().fanoutFailureCode(failureSuffixNotCompleted)
		message := e.fanoutFailureMessage(index, "terminated with status "+result.Status().String())
		failure, err := agent.NewFailure(agent.FailureKindExternal, code, message)
		return &failure, nil, err
	}
	output, present := result.Output()
	if !present {
		failure, err := agent.NewFailure(
			agent.FailureKindContract, e.stage().fanoutFailureCode(failureSuffixOutputMissing),
			e.fanoutFailureMessage(index, "returned no Output"),
		)
		return &failure, nil, err
	}
	if err := e.stage().fanout.outputSchema.Validate(output.JSON()); err != nil {
		failure, failureErr := agent.NewFailure(
			agent.FailureKindContract, e.stage().fanoutFailureCode(failureSuffixOutputInvalid),
			e.fanoutFailureMessage(index, "violated its Output contract"),
		)
		return &failure, nil, failureErr
	}
	return nil, output.JSON(), nil
}

func (e *execution) fanoutFailureMessage(index uint32, diagnostic string) string {
	return string(e.stage().kind) + " Stage " + e.stage().id + " " +
		e.stage().fanoutMemberLabel(index) + " " + diagnostic
}

func (e *execution) firstFanoutFailure() agent.Failure {
	for _, child := range e.state.ActiveFanoutWindow {
		if child.Failure != nil && child.Failure.Valid() {
			return *child.Failure
		}
	}
	return agent.Failure{}
}

func (e *execution) fanoutHasStartedChildren() bool {
	for _, child := range e.state.ActiveFanoutWindow {
		if child.ChildProcessID != nil {
			return true
		}
	}
	return false
}

func (e *execution) fanoutChildKey(index uint32) (agent.ChildKey, error) {
	member, found := e.stage().fanout.source.member(index)
	if !found {
		return agent.ChildKey{}, ErrInvalidExecutionState
	}
	return workflowChildKey(string(e.stage().kind), e.stage().id, member.id)
}

func (e *execution) fanoutWaitKey() (agent.WaitKey, error) {
	return workflowWaitKey(
		string(e.stage().kind), e.stage().id,
		strconv.FormatUint(uint64(e.state.fanoutWindowStart()), 10),
	)
}

func (e *execution) clearFanout() {
	e.state.ActiveFanoutWindow = nil
	e.state.CompletedFanoutOutputs = nil
}

var _ agent.Execution = (*execution)(nil)
