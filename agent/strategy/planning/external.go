package planning

import (
	"context"
	"errors"

	agent "github.com/Tangerg/scope/agent"
)

// SenseRequest is one side-effect-free request for the current complete
// WorldState. Input is the original Planning Process input; EffectID is stable
// for the prepared attempt.
type SenseRequest struct {
	EffectID agent.EffectID
	Input    agent.Payload
}

// ActionRequest is one external dispatcher Action invocation selected against
// an observed WorldState. Input and WorldState are immutable values.
type ActionRequest struct {
	EffectID          agent.EffectID
	Input             agent.Payload
	ActionName        string
	ActionDescription string
	WorldState        WorldState
}

// Sensor errors are definite sensing failures and terminate Planning.
type Sensor interface {
	// Sense obtains one complete immutable WorldState for the original Process
	// input. It must honor ctx and must not cause externally visible side effects,
	// because the same EffectID may be replayed after an unknown sensing outcome.
	Sense(ctx context.Context, request SenseRequest) (WorldState, error)
}

type SensorFunc func(ctx context.Context, request SenseRequest) (WorldState, error)

func (s SensorFunc) Sense(
	ctx context.Context,
	request SenseRequest,
) (WorldState, error) {
	return s(ctx, request)
}

// ActionExecutor reports definite results separately from unknown external outcomes.
type ActionExecutor interface {
	// Execute attempts one selected Action against the observed WorldState. A
	// valid ActionResult is definite; a non-nil error means the external outcome
	// is unknown and must not be translated into an ordinary failed Action or
	// implicitly retried under a new identity.
	Execute(ctx context.Context, request ActionRequest) (ActionResult, error)
}

type ActionExecutorFunc func(ctx context.Context, request ActionRequest) (ActionResult, error)

func (a ActionExecutorFunc) Execute(
	ctx context.Context,
	request ActionRequest,
) (ActionResult, error) {
	return a(ctx, request)
}

// ActionResult is the definite external result reported by an ActionExecutor.
// Its zero value is invalid.
type ActionResult struct {
	succeeded  bool
	diagnostic string
}

func ActionSucceeded() ActionResult { return ActionResult{succeeded: true} }

func ActionFailed(diagnostic string) (ActionResult, error) {
	if !agent.ValidDiagnostic(diagnostic) {
		return ActionResult{}, errors.New("planning: Action failure diagnostic must be non-empty, trimmed, and bounded")
	}
	return ActionResult{diagnostic: diagnostic}, nil
}

func (a ActionResult) Succeeded() bool { return a.succeeded }

// Diagnostic returns the definite failure explanation, or an empty string on
// success.
func (a ActionResult) Diagnostic() string { return a.diagnostic }

func (a ActionResult) Valid() bool {
	if a.succeeded {
		return a.diagnostic == ""
	}
	return agent.ValidDiagnostic(a.diagnostic)
}

// NewActionSettlement closes an investigated action Effect with a definite result.
func NewActionSettlement(effectID agent.EffectID, result ActionResult) (agent.Settlement, error) {
	if !effectID.Valid() || !result.Valid() {
		return agent.Settlement{}, ErrInvalidProtocol
	}
	payload, err := actionSignal(result)
	if err != nil {
		return agent.Settlement{}, err
	}
	status := agent.SettlementStatusSucceeded
	if !result.Succeeded() {
		status = agent.SettlementStatusFailed
	}
	return agent.NewSettlement(effectID, status, payload)
}
