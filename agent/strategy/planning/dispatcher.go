package planning

import (
	"context"
	"fmt"

	"github.com/samber/lo"

	agent "github.com/Tangerg/scope/agent"
)

// DispatcherConfig binds side-effect-free sensing and the exact set of
// dispatcher-targeted Action executors required by a Definition. Child-bound
// Actions must not appear in ActionExecutors.
type DispatcherConfig struct {
	// Sensor supplies each complete WorldState observation.
	Sensor Sensor
	// ActionExecutors maps dispatcher-bound Action names to exact executors.
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

// NewDispatcher binds a definition to the executors that carry out its
// effects. It is constructed against an exact definition so an effect cannot
// be routed to an executor the planner never planned against.
func NewDispatcher(definition *Definition, config DispatcherConfig) (*Dispatcher, error) {
	if !definition.valid() || lo.IsNil(config.Sensor) {
		return nil, ErrInvalidDispatcherConfig
	}
	executors := make(map[string]boundExecutor)
	for _, binding := range definition.bindings {
		executor, supplied := config.ActionExecutors[binding.action.name]
		switch binding.target {
		case bindingTargetDispatcher:
			if !supplied || lo.IsNil(executor) {
				return nil, fmt.Errorf("%w: missing executor for Action %q", ErrInvalidDispatcherConfig, binding.action.name)
			}
			executors[binding.action.name] = boundExecutor{
				action: binding.action, required: binding.required, executor: executor,
			}
		case bindingTargetChild:
			if supplied {
				return nil, fmt.Errorf("%w: child Action %q cannot have an executor", ErrInvalidDispatcherConfig, binding.action.name)
			}
		default:
			return nil, ErrInvalidDispatcherConfig
		}
	}
	for name, executor := range config.ActionExecutors {
		if !validName(name) || lo.IsNil(executor) {
			return nil, fmt.Errorf("%w: invalid executor %q", ErrInvalidDispatcherConfig, name)
		}
		if _, found := executors[name]; !found {
			return nil, fmt.Errorf("%w: extra executor for Action %q", ErrInvalidDispatcherConfig, name)
		}
	}
	return &Dispatcher{
		descriptor: definition.descriptor, sensor: config.Sensor, executors: executors,
	}, nil
}

// Dispatch executes one validated Planning protocol operation. Sensor errors
// and valid ActionResult failures are definite failed settlements; an
// ActionExecutor error leaves the Effect outcome unknown. Action Effects must
// declare every capability required by the frozen binding before execution.
func (d *Dispatcher) Dispatch(
	ctx context.Context,
	request agent.EffectRequest,
	_ agent.DeltaEmitter,
) (agent.Settlement, error) {
	if d == nil || !d.descriptor.Valid() || lo.IsNil(d.sensor) {
		return agent.Settlement{}, ErrInvalidDispatcherConfig
	}
	envelope, err := decodeEffect(request.Effect().Payload())
	if err != nil {
		return agent.Settlement{}, err
	}
	if err := d.descriptor.ValidateInput(envelope.Input); err != nil {
		return agent.Settlement{}, fmt.Errorf("%w: Effect Input: %w", ErrInvalidProtocol, err)
	}
	switch envelope.Operation {
	case operationSense:
		return d.sense(ctx, request.ID(), envelope.Input)
	case operationAction:
		return d.execute(ctx, request, envelope.Input, *envelope.Action)
	default:
		return agent.Settlement{}, ErrInvalidProtocol
	}
}

// ReplayPolicy permits same-identity replay only for side-effect-free
// sensing. Action Effects may have irreversible external consequences and
// always require explicit resolution after an unknown attempt.
func (*Dispatcher) ReplayPolicy(effect agent.Effect) agent.ReplayPolicy {
	envelope, err := decodeEffect(effect.Payload())
	if err == nil && envelope.Operation == operationSense {
		return agent.ReplayPolicySameIdentity
	}
	return agent.ReplayPolicyNever
}

func (d *Dispatcher) sense(
	ctx context.Context,
	effectID agent.EffectID,
	input agent.Input,
) (agent.Settlement, error) {
	request := SenseRequest{EffectID: effectID, Input: input}
	if err := validateSenseRequest(request); err != nil {
		return agent.Settlement{}, err
	}
	state, senseErr := d.sensor.Sense(ctx, request)
	payload, err := senseSignal(state, senseErr)
	if err != nil {
		return agent.Settlement{}, err
	}
	status := agent.SettlementStatusSucceeded
	if senseErr != nil {
		status = agent.SettlementStatusFailed
	}
	return agent.NewSettlement(effectID, status, payload)
}

func (d *Dispatcher) execute(
	ctx context.Context,
	effectRequest agent.EffectRequest,
	input agent.Input,
	call actionCall,
) (agent.Settlement, error) {
	bound, found := d.executors[call.Name]
	if !found || bound.action.description != call.Description || !bound.action.Applicable(call.WorldState) ||
		!effectRequest.Effect().RequiredCapabilities().Allows(bound.required) {
		return agent.Settlement{}, fmt.Errorf("%w: Action %q does not match frozen binding", ErrInvalidProtocol, call.Name)
	}
	request := ActionRequest{
		EffectID: effectRequest.ID(), Input: input, ActionName: call.Name,
		ActionDescription: call.Description, WorldState: call.WorldState,
	}
	if err := validateActionRequest(request); err != nil {
		return agent.Settlement{}, err
	}
	result, err := bound.executor.Execute(ctx, request)
	if err != nil {
		return agent.Settlement{}, fmt.Errorf("planning: execute Action %q: %w", call.Name, err)
	}
	if !result.Valid() {
		return agent.Settlement{}, fmt.Errorf("planning: Action %q returned an invalid result", call.Name)
	}
	return NewActionSettlement(effectRequest.ID(), result)
}

var _ agent.Dispatcher = (*Dispatcher)(nil)
