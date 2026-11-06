package planning

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/agent/run"
)

type dispatcherConfig struct {
	Definition *definition
	Sensor     Sensor
	Executors  []ActionExecutor
}

// Executor slots use the definition's immutable action index; a separate name
// registry could bind different authority and execution to the same Action.
// Concurrent processes require concurrency-safe host implementations.
type dispatcher struct {
	definition *definition
	sensor     Sensor
	executors  []ActionExecutor
}

func newDispatcher(config dispatcherConfig) (*dispatcher, error) {
	if !config.Definition.valid() || lo.IsNil(config.Sensor) || len(config.Executors) != len(config.Definition.bindings) {
		return nil, ErrInvalidDispatcherConfig
	}
	for index, binding := range config.Definition.bindings {
		if binding.delegatesToChild() != lo.IsNil(config.Executors[index]) {
			return nil, fmt.Errorf("%w: Action %q execution owner", ErrInvalidDispatcherConfig, binding.action.name)
		}
	}
	return &dispatcher{definition: config.Definition, sensor: config.Sensor, executors: slices.Clone(config.Executors)}, nil
}

// Dispatch executes one validated Planning protocol operation. Sensor errors
// other than local writer interruption and valid ActionResult failures are
// definite failed settlements; an ActionExecutor error leaves the Effect
// outcome unknown. The Runtime enforces the capabilities Policy declares for
// each Action before dispatch. Input that
// violates the definition schema or an Action whose binding does not admit the
// request's world state returns a failed contract settlement; Execution
// consumes it as a contract failure without another external attempt.
func (d *dispatcher) Dispatch(
	ctx context.Context,
	request run.EffectRequest,
	_ run.DeltaEmitter,
) (run.Settlement, error) {
	ctx = run.RequireContext(ctx)
	if !request.Valid() {
		return run.Settlement{}, ErrInvalidProtocol
	}
	if d == nil || d.definition == nil || lo.IsNil(d.sensor) {
		return planningFailureSettlement(ErrInvalidDispatcherConfig)
	}
	envelope, err := decodeEffect(request.Effect().Payload().JSON())
	if err != nil {
		return planningFailureSettlement(err)
	}
	if err := d.definition.descriptor.ValidateInput(envelope.Input); err != nil {
		return planningFailureSettlement(fmt.Errorf("%w: Effect Input: %w", ErrInvalidProtocol, err))
	}
	switch envelope.operation() {
	case operationSense:
		return d.sense(ctx, request.ID(), envelope.Input)
	case operationAction:
		return d.execute(ctx, request, envelope.Input, *envelope.Action)
	default:
		return planningFailureSettlement(ErrInvalidProtocol)
	}
}

// Sensing is side-effect-free. Action replay authority belongs to its binding.
func (d *dispatcher) Policy(effect run.Effect) run.EffectPolicy {
	envelope, err := decodeEffect(effect.Payload().JSON())
	if err == nil && envelope.operation() == operationSense {
		return run.EffectPolicy{Replay: run.ReplayPolicySameIdentity}
	}
	policy := run.EffectPolicy{Replay: run.ReplayPolicyNever}
	if err == nil && d != nil && d.definition != nil {
		if binding, bound := d.definition.binding(envelope.Action.Name); bound && !binding.delegatesToChild() {
			policy = binding.policy
		}
	}
	return policy
}

func (d *dispatcher) sense(
	ctx context.Context,
	effectID run.EffectID,
	input run.Payload,
) (run.Settlement, error) {
	request := SenseRequest{EffectID: effectID, Input: input}
	state, senseErr := d.sensor.Sense(ctx, request)
	cause := context.Cause(ctx)
	if errors.Is(cause, run.ErrWriterStopped) && (errors.Is(senseErr, ctx.Err()) || errors.Is(senseErr, cause)) {
		return run.Settlement{}, fmt.Errorf("planning: sense: %w", senseErr)
	}
	settlement, err := senseSettlement(state, senseErr)
	if err != nil {
		return planningFailureSettlement(err)
	}
	return settlement, nil
}

func (d *dispatcher) execute(
	ctx context.Context,
	effectRequest run.EffectRequest,
	input run.Payload,
	call actionCall,
) (run.Settlement, error) {
	index, found := d.definition.bindingsByName[call.Name]
	if !found {
		return planningFailureSettlement(fmt.Errorf("%w: Action %q is not bound", ErrInvalidProtocol, call.Name))
	}
	binding := d.definition.bindings[index]
	if binding.delegatesToChild() || !binding.action.Applicable(call.WorldState) {
		return planningFailureSettlement(fmt.Errorf("%w: Action %q does not match frozen binding", ErrInvalidProtocol, call.Name))
	}
	request := ActionRequest{
		EffectID: effectRequest.ID(), Input: input, ActionName: call.Name,
		ActionDescription: binding.action.description, WorldState: call.WorldState,
	}
	result, err := d.executors[index].Execute(ctx, request)
	if err != nil {
		return run.Settlement{}, fmt.Errorf("planning: execute Action %q: %w", call.Name, err)
	}
	if !result.Valid() {
		return run.Settlement{}, fmt.Errorf("planning: Action %q returned an invalid result", call.Name)
	}
	return NewActionSettlement(result)
}

var _ run.Dispatcher = (*dispatcher)(nil)

func planningFailureSettlement(cause error) (run.Settlement, error) {
	return failedSettlement(run.FailureKindContract, failureCodePlanningHostRejected, cause.Error())
}
