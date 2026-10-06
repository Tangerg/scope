package planning

import (
	"context"
	jsonv2 "encoding/json/v2"
	"fmt"

	"github.com/samber/lo"

	agent "github.com/Tangerg/scope/agent"
)

// DispatcherConfig binds side-effect-free sensing and the exact set of
// dispatcher-targeted Action executors required by a Definition. Child-bound
// Actions must not appear in ActionExecutors.
type DispatcherConfig struct {
	Sensor          Sensor
	ActionExecutors map[string]ActionExecutor
}

type boundExecutor struct {
	action   Action
	required agent.CapabilitySet
	executor ActionExecutor
}

// Dispatcher executes sensing and dispatcher Action Effects emitted by one
// Planning Definition. It is immutable after construction and may serve
// Processes concurrently when Sensor and ActionExecutors are concurrent-safe.
type Dispatcher struct {
	descriptor agent.Descriptor
	sensor     Sensor
	executors  map[string]boundExecutor
}

func NewDispatcher(definition *Definition, config DispatcherConfig) (*Dispatcher, error) {
	if !definition.valid() || lo.IsNil(config.Sensor) {
		return nil, ErrInvalidDispatcherConfig
	}
	executors, err := bindExecutors(definition.bindings, config.ActionExecutors)
	if err != nil {
		return nil, err
	}
	return &Dispatcher{
		descriptor: definition.descriptor, sensor: config.Sensor, executors: executors,
	}, nil
}

func bindExecutors(bindings []ActionBinding, supplied map[string]ActionExecutor) (map[string]boundExecutor, error) {
	executors := make(map[string]boundExecutor)
	for _, binding := range bindings {
		executor, found := supplied[binding.action.name]
		switch binding.target {
		case bindingTargetDispatcher:
			if !found || lo.IsNil(executor) {
				return nil, fmt.Errorf("%w: missing executor for Action %q", ErrInvalidDispatcherConfig, binding.action.name)
			}
			executors[binding.action.name] = boundExecutor{
				action: binding.action, required: binding.required, executor: executor,
			}
		case bindingTargetChild:
			if found {
				return nil, fmt.Errorf("%w: child Action %q cannot have an executor", ErrInvalidDispatcherConfig, binding.action.name)
			}
		default:
			return nil, ErrInvalidDispatcherConfig
		}
	}
	for name, executor := range supplied {
		if !agent.ValidQualifiedName(name) || lo.IsNil(executor) {
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
// ActionExecutor error leaves the Effect outcome unknown. Action Effects must
// declare every capability required by the frozen binding before execution.
// Local protocol or binding rejection returns a Failed host_error settlement;
// Execution consumes it as a contract failure without another external attempt.
func (d *Dispatcher) Dispatch(
	ctx context.Context,
	request agent.EffectRequest,
	_ agent.DeltaEmitter,
) (agent.Settlement, error) {
	ctx = agent.RequireContext(ctx)
	if !request.Valid() {
		return agent.Settlement{}, ErrInvalidProtocol
	}
	if d == nil || !d.descriptor.Valid() || lo.IsNil(d.sensor) {
		return planningFailureSettlement(ErrInvalidDispatcherConfig)
	}
	envelope, err := decodeEffect(request.Effect().Payload())
	if err != nil {
		return planningFailureSettlement(err)
	}
	if err := d.descriptor.ValidateInput(envelope.Input); err != nil {
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

// ReplayPolicy permits same-identity replay only for side-effect-free
// sensing. Action Effects may have irreversible external consequences and
// always require explicit resolution after an unknown attempt.
func (*Dispatcher) ReplayPolicy(effect agent.Effect) agent.ReplayPolicy {
	envelope, err := decodeEffect(effect.Payload())
	if err == nil && envelope.operation() == operationSense {
		return agent.ReplayPolicySameIdentity
	}
	return agent.ReplayPolicyNever
}

func (d *Dispatcher) sense(
	ctx context.Context,
	effectID agent.EffectID,
	input agent.Payload,
) (agent.Settlement, error) {
	request := SenseRequest{EffectID: effectID, Input: input}
	state, senseErr := d.sensor.Sense(ctx, request)
	payload, err := senseSignal(state, senseErr)
	if err != nil {
		return planningFailureSettlement(err)
	}
	status := agent.SettlementStatusSucceeded
	if senseErr != nil {
		status = agent.SettlementStatusFailed
	}
	return agent.NewSettlement(status, payload)
}

func (d *Dispatcher) execute(
	ctx context.Context,
	effectRequest agent.EffectRequest,
	input agent.Payload,
	call actionCall,
) (agent.Settlement, error) {
	bound, found := d.executors[call.Name]
	if !found || bound.action.description != call.Description || !bound.action.Applicable(call.WorldState) ||
		!effectRequest.Effect().RequiredCapabilities().Allows(bound.required) {
		return planningFailureSettlement(fmt.Errorf("%w: Action %q does not match frozen binding", ErrInvalidProtocol, call.Name))
	}
	request := ActionRequest{
		EffectID: effectRequest.ID(), Input: input, ActionName: call.Name,
		ActionDescription: call.Description, WorldState: call.WorldState,
	}
	result, err := bound.executor.Execute(ctx, request)
	if err != nil {
		return agent.Settlement{}, fmt.Errorf("planning: execute Action %q: %w", call.Name, err)
	}
	if !result.Valid() {
		return agent.Settlement{}, fmt.Errorf("planning: Action %q returned an invalid result", call.Name)
	}
	return NewActionSettlement(result)
}

var _ agent.Dispatcher = (*Dispatcher)(nil)

func planningFailureSettlement(cause error) (agent.Settlement, error) {
	payload, err := jsonv2.Marshal(signalEnvelope{HostError: agent.NormalizeDiagnostic(cause.Error())})
	if err != nil {
		return agent.Settlement{}, err
	}
	return agent.NewSettlement(agent.SettlementStatusFailed, payload)
}
