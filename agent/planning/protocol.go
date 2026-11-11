package planning

import (
	"fmt"

	"github.com/Tangerg/scope/agent/internal/strategy/stepfail"
	"github.com/Tangerg/scope/agent/run"
)

type operation string

const (
	operationSense  operation = "sense"
	operationAction operation = "action"
)

// effectEnvelope is a sense request unless it names an Action.
type effectEnvelope struct {
	Input  run.Payload `json:"input"`
	Action *actionCall `json:"action,omitzero"`
}

func (e effectEnvelope) operation() operation {
	if e.Action != nil {
		return operationAction
	}
	return operationSense
}

// actionCall names the bound Action; the binding owns its description.
type actionCall struct {
	Name       string     `json:"name"`
	WorldState WorldState `json:"world_state"`
}

// The settlement status owns whether an operation succeeded. A successful
// sensing settlement carries the observed world state and a successful Action
// settlement carries nothing; a failed settlement carries run.Failure.
type senseResult struct {
	WorldState WorldState `json:"world_state" jsonwire:"required"`
}

type actionCompleted struct{}

// Execution records these settlement failures unchanged, so each code is the
// one name of its fact in every Log and Output.
const (
	failureCodePlanningDispatchRejected = "planning.dispatch.rejected"
	failureCodePlanningSensingFailed    = "planning.sensing.failed"
	failureCodePlanningActionFailed     = "planning.action.failed"
)

func newSenseEffect(input run.Payload) (run.Effect, error) {
	payload, err := run.EncodePayload(effectEnvelope{Input: input})
	if err != nil {
		return run.Effect{}, err
	}
	return run.NewDispatcherEffect(payload)
}

// newActionEffect declares a dispatcher Action the Planner selected. Its plan
// passed EvaluatePlan against the current state, so the Action applies.
func newActionEffect(input run.Payload, binding actionDeclaration, state WorldState) (run.Effect, error) {
	payload, err := run.EncodePayload(effectEnvelope{
		Input: input,
		Action: &actionCall{
			Name: binding.action.name, WorldState: state,
		},
	})
	if err != nil {
		return run.Effect{}, err
	}
	return run.NewDispatcherEffect(payload)
}

func decodeEffect(payload run.Payload) (effectEnvelope, error) {
	envelope, err := payload.Decode[effectEnvelope]()
	if err != nil {
		return effectEnvelope{}, fmt.Errorf("%w: decode Effect: %w", ErrInvalidProtocol, err)
	}
	if !envelope.valid() {
		return effectEnvelope{}, ErrInvalidProtocol
	}
	return envelope, nil
}

func (e effectEnvelope) valid() bool {
	if !e.Input.Valid() {
		return false
	}
	return e.Action == nil || run.ValidQualifiedName(e.Action.Name)
}

func senseSettlement(state WorldState, cause error) (run.Settlement, error) {
	if cause != nil {
		return stepfail.Settlement(run.FailureKindExternal, failureCodePlanningSensingFailed, cause.Error())
	}
	payload, err := run.EncodePayload(senseResult{WorldState: state})
	if err != nil {
		return run.Settlement{}, err
	}
	return run.NewSettlement(run.SettlementStatusSucceeded, payload)
}

// decodeSettlement reads the single dispatcher settlement of the awaited
// operation. A failed settlement returns its explanation instead of a result.
func decodeSettlement[T any](messages []run.Message, expected run.EffectID) (T, *run.Failure, error) {
	var result T
	if len(messages) != 1 {
		return result, nil, fmt.Errorf("%w: exactly one settlement Signal is required", ErrInvalidProtocol)
	}
	completion, ok := messages[0].(run.Completion)
	if !ok || completion.Effect != expected {
		return result, nil, ErrInvalidProtocol
	}
	settlement := completion.Outcome
	if failure, failed := settlement.Failure(); failed {
		return result, &failure, nil
	}
	result, err := settlement.Payload().Decode[T]()
	if err != nil {
		return result, nil, fmt.Errorf("%w: decode settlement: %w", ErrInvalidProtocol, err)
	}
	return result, nil, nil
}
