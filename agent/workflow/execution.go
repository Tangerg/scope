package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	agent "github.com/Tangerg/scope/agent"
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
	if err := e.state.validate(e.definition); err != nil {
		return agent.Transition{}, err
	}
	switch e.state.Phase {
	case phaseReady:
		return e.advance(ctx, signals)
	case phaseAwaitingChildStart:
		return e.acceptChildStart(signals)
	case phaseAwaitingChildWaitOpen:
		return e.acceptChildWaitOpen(signals)
	case phaseWaitingChild:
		return e.acceptChildCompletion(ctx, signals)
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
	if err := e.state.validate(e.definition); err != nil {
		return agent.ExecutionState{}, err
	}
	return encodeExecutionState(e.state)
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
	case StageKindFork:
		return e.startFanoutWindow(0)
	case StageKindMap:
		count, err := stage.fanoutCount(e.state.CurrentValue)
		if err != nil {
			if _, ok := errors.AsType[mapMaxItemsExceededError](err); ok {
				return e.failContract(
					0, stage.failureCode("max_items_exceeded"),
					"Map Stage "+stage.id+" input exceeds its configured maximum items",
				)
			}
			return agent.Transition{}, err
		}
		if count > 0 {
			return e.startFanoutWindow(0)
		}
		value, err := stage.fanoutComplete(ctx, []json.RawMessage{})
		if err != nil {
			return agent.Transition{}, err
		}
		e.state.CurrentValue = value
		return e.finishStage(0)
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
	e.state.Phase = phaseAwaitingChildStart
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
		return stage.loop.binding, stage.loop.valid()
	default:
		return childBinding{}, false
	}
}

func (e *execution) clearSingleChild() {
	e.state.SelectedCaseID = ""
	e.state.ChildProcessID = nil
	e.state.WaitID = nil
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

func (e *execution) acceptChildStart(signals []agent.Signal) (agent.Transition, error) {
	if len(signals) == 0 {
		return agent.Transition{}, fmt.Errorf("%w: child start requires its settlement Signal", ErrInvalidProtocol)
	}
	result, err := agent.ParseChildStartResult(signals[0])
	key, keyErr := e.childKey()
	binding, bound := e.singleChildBinding()
	stage := e.stage()
	if err != nil || keyErr != nil || !bound || result.Key() != key || result.DeploymentRef() != binding.deploymentRef {
		return agent.Transition{}, fmt.Errorf("%w: child-start result does not match Stage %q", ErrInvalidProtocol, stage.id)
	}
	if failure, failed := result.Failure(); failed {
		return e.fail(
			1, stage.failureCode("start_failed"),
			"Child Process start failed for Stage "+e.stageInvocationLabel()+": "+failure.Code(),
			failure.Kind(),
		)
	}
	childID, started := result.ProcessID()
	if !started {
		return agent.Transition{}, fmt.Errorf("%w: child-start result has no Process", ErrInvalidProtocol)
	}
	waitKey, err := e.waitKey()
	if err != nil {
		return agent.Transition{}, err
	}
	effect, err := agent.WaitForChildren(agent.ChildWaitSpec{
		Boundary: agent.ChildWaitBoundaryDrained,
		Key:      waitKey, Children: []agent.ProcessID{childID}, Condition: agent.AllChildren(),
	})
	if err != nil {
		return agent.Transition{}, err
	}
	e.state.ChildProcessID = &childID
	e.state.Phase = phaseAwaitingChildWaitOpen
	return agent.Continue(1, effect)
}

func (e *execution) acceptChildWaitOpen(signals []agent.Signal) (agent.Transition, error) {
	if len(signals) == 0 || e.state.ChildProcessID == nil {
		return agent.Transition{}, fmt.Errorf("%w: child wait opening requires its settlement Signal", ErrInvalidProtocol)
	}
	opened, err := agent.ParseChildWaitOpened(signals[0])
	wantKey, keyErr := e.waitKey()
	spec := opened.Spec()
	if err != nil || keyErr != nil || spec.Key != wantKey || spec.Boundary != agent.ChildWaitBoundaryDrained || len(spec.Children) != 1 ||
		spec.Children[0] != *e.state.ChildProcessID || spec.Condition != agent.AllChildren() {
		return agent.Transition{}, fmt.Errorf("%w: child wait opening does not match Stage %q", ErrInvalidProtocol, e.stage().id)
	}
	waitID := opened.WaitID()
	e.state.WaitID = &waitID
	e.state.Phase = phaseWaitingChild
	return agent.Wait(1, waitID)
}

func (e *execution) acceptChildCompletion(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	if len(signals) == 0 || e.state.ChildProcessID == nil || e.state.WaitID == nil {
		return agent.Transition{}, fmt.Errorf("%w: child completion requires one active child wait Signal", ErrInvalidProtocol)
	}
	completed, err := agent.ParseChildWaitSatisfied(signals[0])
	wantWaitKey, keyErr := e.waitKey()
	if err != nil || keyErr != nil || completed.WaitID() != *e.state.WaitID || completed.Key() != wantWaitKey || completed.Boundary() != agent.ChildWaitBoundaryDrained {
		return agent.Transition{}, fmt.Errorf("%w: child completion does not match Stage %q", ErrInvalidProtocol, e.stage().id)
	}
	outcomes := completed.Outcomes()
	wantChildKey, err := e.childKey()
	if err != nil || len(outcomes) != 1 || outcomes[0].Key() != wantChildKey ||
		outcomes[0].Result().ProcessID() != *e.state.ChildProcessID {
		return agent.Transition{}, fmt.Errorf("%w: child outcome does not match Stage %q", ErrInvalidProtocol, e.stage().id)
	}
	result := outcomes[0].Result()
	if result.Status() != agent.StatusCompleted {
		if failure, failed := result.Termination().Failure(); failed {
			return e.failExternal(
				1, e.stage().failureCode("child_failed"),
				"Child Process failed for Stage "+e.stageInvocationLabel()+": "+failure.Code(),
			)
		}
		return e.failExternal(
			1,
			e.stage().failureCode("child_not_completed"),
			"Child Process for Stage "+e.stageInvocationLabel()+" terminated with status "+result.Status().String(),
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

func (e *execution) failExternal(consumedSignals uint32, code, message string) (agent.Transition, error) {
	return e.fail(consumedSignals, code, message, agent.FailureKindExternal)
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

var _ agent.Execution = (*execution)(nil)
