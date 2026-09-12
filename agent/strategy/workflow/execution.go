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
	if e == nil || !e.definition.valid() {
		return agent.ExecutionState{}, ErrInvalidExecutionState
	}
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
					0, stage.failureCode("case_unknown"),
					"Switch Stage "+stage.id+" selected an undeclared case",
				)
			}
			return agent.Transition{}, err
		}
		binding, found := stage.switcher.binding(selected)
		if !found {
			return agent.Transition{}, ErrInvalidStage
		}
		e.state.SelectedCaseID = selected
		return e.startSingleChild(0, binding)
	case StageKindFork, StageKindMap:
		return e.startFanoutWindow(ctx, 0)
	case StageKindLoop:
		e.state.LoopIteration = 1
		return e.startSingleChild(0, stage.loop.binding)
	default:
		return agent.Transition{}, ErrInvalidStage
	}
}

func (e *execution) startSingleChild(consumedSignals uint32, binding childBinding) (agent.Transition, error) {
	input, err := agent.ParseInput(e.state.CurrentValue)
	if err != nil {
		return agent.Transition{}, err
	}
	key, err := e.childKey()
	if err != nil {
		return agent.Transition{}, err
	}
	effect, err := agent.StartChild(agent.ChildSpec{
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
		return e.stage().id + ".iteration." + strconv.FormatUint(uint64(e.state.LoopIteration), 10)
	}
	return e.stage().id
}

func (e *execution) advanceChild(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	if len(signals) == 0 {
		return agent.Transition{}, fmt.Errorf("%w: child handshake requires its settlement Signal", ErrInvalidProtocol)
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
		return e.acceptChildStart(signals[0], key, waitKey)
	case childcall.AwaitingOpening:
		waitID, openErr := e.state.Child.AcceptOpening(signals[0], waitKey, agent.ChildWaitBoundaryDrained)
		if openErr != nil {
			return agent.Transition{}, fmt.Errorf("%w: Stage %q child wait opening: %w", ErrInvalidProtocol, e.stage().id, openErr)
		}
		return agent.Wait(1, waitID)
	default:
		result, completeErr := e.state.Child.Complete(signals[0], key, waitKey, agent.ChildWaitBoundaryDrained)
		if completeErr != nil {
			return agent.Transition{}, fmt.Errorf("%w: Stage %q child completion: %w", ErrInvalidProtocol, e.stage().id, completeErr)
		}
		return e.acceptChildCompletion(ctx, result)
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

func (e *execution) acceptChildCompletion(ctx context.Context, result agent.Result) (agent.Transition, error) {
	if result.Status() != agent.StatusCompleted {
		if failure, failed := result.Termination().Failure(); failed {
			return agent.Fail(1, failure)
		}
		return e.fail(
			1,
			e.stage().failureCode("child_not_completed"),
			"Child Process for Stage "+e.stageInvocationLabel()+" terminated with status "+result.Status().String(),
			agent.FailureKindExternal,
		)
	}
	output, present := result.Output()
	if !present {
		return e.failContract(1, e.stage().failureCode("output_missing"), "Completed child Process returned no Output")
	}
	if err := e.singleChildOutputSchema().ValidateOutput(output); err != nil {
		return e.failContract(1, e.stage().failureCode("output_invalid"), "Child Process Output violated the Stage contract")
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
	output, err := agent.ParseOutput(e.state.CurrentValue)
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
		strconv.FormatUint(uint64(e.state.LoopIteration), 10),
	)
}

func (e *execution) waitKey() (agent.WaitKey, error) {
	return workflowWaitKey(
		"single", e.stage().id, e.state.SelectedCaseID,
		strconv.FormatUint(uint64(e.state.LoopIteration), 10),
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
	output agent.Output,
) (agent.Transition, error) {
	stage := e.stage()
	satisfied, err := stage.loop.predicate(ctx, output.JSON())
	if err != nil {
		return agent.Transition{}, err
	}
	e.state.CurrentValue = output.JSON()
	if satisfied || e.state.LoopIteration == stage.loop.maxIterations {
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
	e.state.LoopIteration++
	return e.startSingleChild(consumedSignals, stage.loop.binding)
}

func (e *execution) startFanoutWindow(ctx context.Context, consumedSignals uint32) (agent.Transition, error) {
	stage := e.stage()
	start := e.state.fanoutWindowStart()
	inputs, count, err := stage.fanout.source.windowInputs(e.state.CurrentValue, start, stage.fanout.windowSize)
	if err != nil {
		if _, exceeded := errors.AsType[mapMaxItemsExceededError](err); exceeded {
			return e.failContract(consumedSignals, stage.failureCode("max_items_exceeded"),
				"Map Stage "+stage.id+" input exceeds its configured maximum items")
		}
		return agent.Transition{}, err
	}
	if start > count || uint32(len(inputs)) != min(stage.fanout.windowSize, count-start) {
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
		member, found := stage.fanout.source.member(index)
		if !found {
			return agent.Transition{}, ErrInvalidStage
		}
		input := inputs[index-start]
		key, err := e.fanoutChildKey(index)
		if err != nil {
			return agent.Transition{}, err
		}
		effect, err := agent.StartChild(agent.ChildSpec{
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

func (e *execution) acceptFanoutStarts(signals []agent.Signal) (agent.Transition, error) {
	window := e.state.ActiveFanoutWindow
	if len(signals) < len(window) {
		return agent.Transition{}, fmt.Errorf("%w: fan-out child starts require one settlement Signal per member", ErrInvalidProtocol)
	}
	childIDs := make([]agent.ProcessID, 0, len(window))
	for offset := range window {
		index := e.state.fanoutWindowStart() + uint32(offset)
		member, found := e.stage().fanout.source.member(index)
		result, err := agent.ParseChildStartResult(signals[offset])
		key, keyErr := e.fanoutChildKey(index)
		if err != nil {
			return agent.Transition{}, fmt.Errorf("%w: fan-out child start: %w", ErrInvalidProtocol, err)
		}
		if keyErr != nil {
			return agent.Transition{}, fmt.Errorf("%w: fan-out child key: %w", ErrInvalidProtocol, keyErr)
		}
		if !found ||
			!childcall.StartMatches(result, key, member.binding.deploymentRef) {
			return agent.Transition{}, fmt.Errorf(
				"%w: %s Stage %q member %q start result mismatch",
				ErrInvalidProtocol, e.stage().kind, e.stage().id, member.id,
			)
		}
		if failure, failed := result.Failure(); failed {
			e.state.ActiveFanoutWindow[offset].Failure = &failure
			continue
		}
		processID, started := result.ProcessID()
		if !started {
			return agent.Transition{}, fmt.Errorf("%w: fan-out child-start result has no Process", ErrInvalidProtocol)
		}
		e.state.ActiveFanoutWindow[offset].ChildProcessID = &processID
		childIDs = append(childIDs, processID)
	}
	consumedSignals := uint32(len(window))
	if len(childIDs) == 0 {
		return agent.Fail(consumedSignals, e.firstFanoutFailure())
	}
	waitKey, err := e.fanoutWaitKey()
	if err != nil {
		return agent.Transition{}, err
	}
	effect, err := agent.WaitForChildren(agent.ChildWaitSpec{
		Boundary: agent.ChildWaitBoundaryDrained,
		Key:      waitKey, Children: childIDs, Condition: agent.AllChildren(),
	})
	if err != nil {
		return agent.Transition{}, err
	}
	e.state.Phase = phaseAwaitingFanoutWaitOpen
	return agent.Continue(consumedSignals, effect)
}

func (e *execution) acceptFanoutWaitOpen(signals []agent.Signal) (agent.Transition, error) {
	if len(signals) == 0 {
		return agent.Transition{}, fmt.Errorf("%w: fan-out wait opening requires its settlement Signal", ErrInvalidProtocol)
	}
	opened, err := agent.ParseChildWaitOpened(signals[0])
	wantKey, keyErr := e.fanoutWaitKey()
	wantChildren := e.fanoutStartedChildren()
	if err != nil {
		return agent.Transition{}, fmt.Errorf("%w: fan-out child wait does not match Stage %q: %w", ErrInvalidProtocol, e.stage().id, err)
	}
	if keyErr != nil {
		return agent.Transition{}, fmt.Errorf("%w: fan-out child wait does not match Stage %q: %w", ErrInvalidProtocol, e.stage().id, keyErr)
	}
	if !childcall.OpeningMatches(opened, agent.ChildWaitSpec{
		Key: wantKey, Boundary: agent.ChildWaitBoundaryDrained,
		Children: wantChildren, Condition: agent.AllChildren(),
	}) {
		return agent.Transition{}, fmt.Errorf("%w: fan-out child wait does not match Stage %q", ErrInvalidProtocol, e.stage().id)
	}
	waitID := opened.WaitID()
	e.state.FanoutWaitID = &waitID
	e.state.Phase = phaseWaitingFanout
	return agent.Wait(1, waitID)
}

func (e *execution) acceptFanoutCompletion(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	if len(signals) == 0 || e.state.FanoutWaitID == nil {
		return agent.Transition{}, fmt.Errorf("%w: fan-out completion requires one active child wait Signal", ErrInvalidProtocol)
	}
	completed, err := agent.ParseChildWaitSatisfied(signals[0])
	wantKey, keyErr := e.fanoutWaitKey()
	if err != nil {
		return agent.Transition{}, fmt.Errorf("%w: fan-out completion does not match Stage %q: %w", ErrInvalidProtocol, e.stage().id, err)
	}
	if keyErr != nil {
		return agent.Transition{}, fmt.Errorf("%w: fan-out completion does not match Stage %q: %w", ErrInvalidProtocol, e.stage().id, keyErr)
	}
	if !childcall.CompletionMatches(completed, *e.state.FanoutWaitID, wantKey, agent.ChildWaitBoundaryDrained) {
		return agent.Transition{}, fmt.Errorf("%w: fan-out completion does not match Stage %q", ErrInvalidProtocol, e.stage().id)
	}
	outcomes := completed.Outcomes()
	if len(outcomes) != len(e.fanoutStartedChildren()) {
		return agent.Transition{}, fmt.Errorf("%w: fan-out completion outcome count mismatch", ErrInvalidProtocol)
	}
	windowOutputs := make([]json.RawMessage, len(e.state.ActiveFanoutWindow))
	outcomeIndex := 0
	for offset := range e.state.ActiveFanoutWindow {
		child := &e.state.ActiveFanoutWindow[offset]
		if child.ChildProcessID == nil {
			continue
		}
		outcome := outcomes[outcomeIndex]
		outcomeIndex++
		index := e.state.fanoutWindowStart() + uint32(offset)
		wantChildKey, fanoutChildKeyErr := e.fanoutChildKey(index)
		if fanoutChildKeyErr != nil {
			return agent.Transition{}, fmt.Errorf("%w: fan-out member outcome mismatch: %w", ErrInvalidProtocol, fanoutChildKeyErr)
		}
		if !childcall.OutcomeMatches(outcome, wantChildKey, *child.ChildProcessID) {
			return agent.Transition{}, fmt.Errorf("%w: fan-out member outcome mismatch", ErrInvalidProtocol)
		}
		failure, output, outcomeErr := e.fanoutOutcome(index, outcome.Result())
		if outcomeErr != nil {
			return agent.Transition{}, outcomeErr
		}
		if failure != nil {
			child.Failure = failure
			continue
		}
		windowOutputs[offset] = output
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
	result agent.Result,
) (*agent.Failure, json.RawMessage, error) {
	if result.Status() != agent.StatusCompleted {
		if failure, failed := result.Termination().Failure(); failed {
			return &failure, nil, nil
		}
		code := e.stage().fanoutFailureCode("not_completed")
		message := e.fanoutFailureMessage(index, "terminated with status "+result.Status().String())
		failure, err := agent.NewFailure(agent.FailureKindExternal, code, message)
		return &failure, nil, err
	}
	output, present := result.Output()
	if !present {
		failure, err := agent.NewFailure(
			agent.FailureKindContract, e.stage().fanoutFailureCode("output_missing"),
			e.fanoutFailureMessage(index, "returned no Output"),
		)
		return &failure, nil, err
	}
	if err := e.stage().fanout.outputSchema.ValidateOutput(output); err != nil {
		failure, failureErr := agent.NewFailure(
			agent.FailureKindContract, e.stage().fanoutFailureCode("output_invalid"),
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

func (e *execution) fanoutStartedChildren() []agent.ProcessID {
	children := make([]agent.ProcessID, 0, len(e.state.ActiveFanoutWindow))
	for _, child := range e.state.ActiveFanoutWindow {
		if child.ChildProcessID != nil {
			children = append(children, *child.ChildProcessID)
		}
	}
	return children
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
