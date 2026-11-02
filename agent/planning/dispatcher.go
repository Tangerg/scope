package planning

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/agent/run"
)

// dispatcherConfig binds side-effect-free sensing and the exact set of
// dispatcher-targeted Action executors required by a definition. Child-bound
// Actions must not appear in ActionExecutors.
type dispatcherConfig struct {
	Sensor          Sensor
	ActionExecutors map[string]ActionExecutor
}

// dispatcher executes sensing and dispatcher Action Effects emitted by one
// Planning definition, whose bindings own each Action's contract and required
// capabilities. It is immutable after construction and may serve Processes
// concurrently when Sensor and ActionExecutors are concurrent-safe.
type dispatcher struct {
	definition *definition
	sensor     Sensor
	executors  map[string]ActionExecutor
}

func (d *dispatcher) bound(name string) (ActionBinding, ActionExecutor, bool) {
	binding, found := d.definition.binding(name)
	executor, bound := d.executors[name]
	return binding, executor, found && bound
}

func newDispatcher(definition *definition, config dispatcherConfig) (*dispatcher, error) {
	if !definition.valid() || lo.IsNil(config.Sensor) {
		return nil, ErrInvalidDispatcherConfig
	}
	executors, err := bindExecutors(definition.bindings, config.ActionExecutors)
	if err != nil {
		return nil, err
	}
	return &dispatcher{definition: definition, sensor: config.Sensor, executors: executors}, nil
}

func bindExecutors(bindings []ActionBinding, supplied map[string]ActionExecutor) (map[string]ActionExecutor, error) {
	executors := make(map[string]ActionExecutor)
	for _, binding := range bindings {
		executor, found := supplied[binding.action.name]
		if binding.delegatesToChild() {
			if found {
				return nil, fmt.Errorf("%w: child Action %q cannot have an executor", ErrInvalidDispatcherConfig, binding.action.name)
			}
			continue
		}
		if !found || lo.IsNil(executor) {
			return nil, fmt.Errorf("%w: missing executor for Action %q", ErrInvalidDispatcherConfig, binding.action.name)
		}
		executors[binding.action.name] = executor
	}
	for _, name := range slices.Sorted(maps.Keys(supplied)) {
		if executor := supplied[name]; !run.ValidQualifiedName(name) || lo.IsNil(executor) {
			return nil, fmt.Errorf("%w: invalid executor %q", ErrInvalidDispatcherConfig, name)
		}
		if _, found := executors[name]; !found {
			return nil, fmt.Errorf("%w: extra executor for Action %q", ErrInvalidDispatcherConfig, name)
		}
	}
	return executors, nil
}

// Dispatch executes one validated Planning protocol operation. Sensor errors
// and valid ActionResult failures are definite failed settlements; an
// ActionExecutor error leaves the Effect outcome unknown. The Runtime enforces
// the capabilities Policy declares for each Action before dispatch. Input that
// violates the definition schema or an Action whose binding does not admit the
// request's world state returns a Failed host_error settlement; Execution
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

// Policy permits same-identity replay only for side-effect-free sensing.
// Action Effects may have irreversible external consequences and always
// require explicit resolution after an unknown attempt; each requires the
// capabilities its frozen binding declares.
func (d *dispatcher) Policy(effect run.Effect) run.EffectPolicy {
	envelope, err := decodeEffect(effect.Payload().JSON())
	if err == nil && envelope.operation() == operationSense {
		return run.EffectPolicy{Replay: run.ReplayPolicySameIdentity}
	}
	policy := run.EffectPolicy{Replay: run.ReplayPolicyNever}
	if err == nil && d != nil && d.definition != nil {
		if binding, _, bound := d.bound(envelope.Action.Name); bound {
			policy.RequiredCapabilities = binding.required
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
	binding, executor, found := d.bound(call.Name)
	if !found || !binding.action.Applicable(call.WorldState) {
		return planningFailureSettlement(fmt.Errorf("%w: Action %q does not match frozen binding", ErrInvalidProtocol, call.Name))
	}
	request := ActionRequest{
		EffectID: effectRequest.ID(), Input: input, ActionName: call.Name,
		ActionDescription: binding.action.description, WorldState: call.WorldState,
	}
	result, err := executor.Execute(ctx, request)
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
	return failedSettlement(settlementFailure{HostError: run.NormalizeDiagnostic(cause.Error())})
}
