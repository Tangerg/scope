package workflow

import (
	"context"
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

var _ agent.Execution = (*execution)(nil)
