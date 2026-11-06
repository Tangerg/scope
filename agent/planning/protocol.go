package planning

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
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

const (
	failureCodePlanningHostRejected    = "planning.host.rejected"
	failureCodePlanningOperationFailed = "planning.operation.failed"
)

func newSenseEffect(input run.Payload) (run.Effect, error) {
	if !input.Valid() {
		return run.Effect{}, ErrInvalidProtocol
	}
	payload, err := jsonv2.Marshal(effectEnvelope{Input: input}, jsonv2.Deterministic(true))
	if err != nil {
		return run.Effect{}, err
	}
	return run.NewDispatcherEffect(payload)
}

func newActionEffect(input run.Payload, binding actionDeclaration, state WorldState) (run.Effect, error) {
	if !input.Valid() || !binding.valid() || binding.delegatesToChild() ||
		!binding.action.Applicable(state) {
		return run.Effect{}, ErrInvalidProtocol
	}
	payload, err := jsonv2.Marshal(effectEnvelope{
		Input: input,
		Action: &actionCall{
			Name: binding.action.name, WorldState: state,
		},
	}, jsonv2.Deterministic(true))
	if err != nil {
		return run.Effect{}, err
	}
	return run.NewDispatcherEffect(payload)
}

func decodeEffect(payload json.RawMessage) (effectEnvelope, error) {
	envelope, err := jsonwire.Decode[effectEnvelope](payload)
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
		return failedSettlement(run.FailureKindExternal, failureCodePlanningOperationFailed, cause.Error())
	}
	payload, err := jsonv2.Marshal(senseResult{WorldState: state}, jsonv2.Deterministic(true))
	if err != nil {
		return run.Settlement{}, err
	}
	return run.NewSettlement(run.SettlementStatusSucceeded, payload)
}

func failedSettlement(kind run.FailureKind, code, diagnostic string) (run.Settlement, error) {
	failure, err := run.NewFailure(kind, code, run.NormalizeDiagnostic(diagnostic))
	if err != nil {
		return run.Settlement{}, err
	}
	return run.NewFailedSettlement(failure, run.Payload{})
}

// decodeSettlement reads the single dispatcher settlement of the awaited
// operation. A failed settlement returns its explanation instead of a result.
func decodeSettlement[T any](messages []run.Message, expected *run.EffectID) (T, *run.Failure, error) {
	var result T
	if len(messages) != 1 {
		return result, nil, fmt.Errorf("%w: exactly one settlement Signal is required", ErrInvalidProtocol)
	}
	completion, ok := messages[0].(run.Completion)
	if !ok || expected == nil || completion.Effect != *expected {
		return result, nil, ErrInvalidProtocol
	}
	settlement := completion.Outcome
	var err error
	if settlement.Status() == run.SettlementStatusFailed {
		failure, valid := settlement.Failure()
		if !valid {
			return result, nil, fmt.Errorf("%w: invalid settlement failure", ErrInvalidProtocol)
		}
		return result, &failure, nil
	}
	if result, err = jsonwire.Decode[T](settlement.Payload().JSON()); err != nil {
		return result, nil, fmt.Errorf("%w: decode settlement: %w", ErrInvalidProtocol, err)
	}
	return result, nil, nil
}
