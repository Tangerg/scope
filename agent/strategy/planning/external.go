package planning

import (
	"context"
	"errors"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
)

// SenseRequest is one side-effect-free request for the current complete
// WorldState. Input is the original Planning Process input; EffectID is stable
// for the prepared attempt.
type SenseRequest struct {
	// EffectID is the stable identity of the prepared sensing attempt.
	EffectID agent.EffectID
	// Input is the original immutable Planning Process input.
	Input agent.Input
}

// ActionRequest is one external dispatcher Action invocation selected against
// an observed WorldState. Input and WorldState are immutable values.
type ActionRequest struct {
	// EffectID is the stable identity of the prepared Action attempt.
	EffectID agent.EffectID
	// Input is the original immutable Planning Process input.
	Input agent.Input
	// ActionName is the exact frozen Action identity.
	ActionName string
	// ActionDescription is the human-readable description of the exact bound Action.
	ActionDescription string
	// WorldState is the complete observation against which the Action was selected.
	WorldState WorldState
}

// Sensor produces one complete WorldState without externally visible side
// effects. A returned error is a definite sensing failure and terminates
// Planning; a Sensor must not use error to report an unknown side effect.
type Sensor interface {
	// Sense obtains one complete immutable WorldState for the original Process
	// input. It must honor ctx and must not cause externally visible side effects,
	// because the same EffectID may be replayed after an unknown sensing outcome.
	Sense(ctx context.Context, request SenseRequest) (WorldState, error)
}

// SensorFunc adapts a plain function to the sensor interface. Sensing
// is an Effect rather than part of a Step, because reading the world is
// external I/O and its result must arrive as a settlement the Execution can be
// resumed from.
type SensorFunc func(ctx context.Context, request SenseRequest) (WorldState, error)

func (s SensorFunc) Sense(
	ctx context.Context,
	request SenseRequest,
) (WorldState, error) {
	return s(ctx, request)
}

// ActionExecutor performs one dispatcher-bound Action. A valid ActionResult is
// a definite success or failure. A non-nil error means the external outcome is
// unknown, so Dispatcher returns an unknown Effect settlement and never retries
// it implicitly.
type ActionExecutor interface {
	// Execute attempts one selected Action against the observed WorldState. A
	// valid ActionResult is definite; a non-nil error means the external outcome
	// is unknown and must not be translated into an ordinary failed Action or
	// implicitly retried under a new identity.
	Execute(ctx context.Context, request ActionRequest) (ActionResult, error)
}

// ActionExecutorFunc adapts a plain function to the executor interface, so a
// single action does not require a named type to be made runnable.
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
	valid      bool
}

// ActionSucceeded returns a definite successful Action result.
func ActionSucceeded() ActionResult { return ActionResult{succeeded: true, valid: true} }

// ActionFailed constructs a definite failed Action result with a bounded
// diagnostic suitable for a portable Planning attempt record.
func ActionFailed(diagnostic string) (ActionResult, error) {
	if !validDiagnostic(diagnostic) {
		return ActionResult{}, errors.New("planning: Action failure diagnostic must be non-empty, trimmed, and bounded")
	}
	return ActionResult{diagnostic: diagnostic, valid: true}, nil
}

// Succeeded reports whether the definite Action result succeeded.
func (a ActionResult) Succeeded() bool { return a.valid && a.succeeded }

// Diagnostic returns the definite failure explanation, or an empty string on
// success.
func (a ActionResult) Diagnostic() string { return a.diagnostic }

func (a ActionResult) Valid() bool {
	return a.valid && (a.succeeded && a.diagnostic == "" ||
		!a.succeeded && validDiagnostic(a.diagnostic))
}

// NewActionSettlement converts an executor result into the kernel settlement
// that closes the effect. Going through this constructor is what keeps an
// executor from encoding planning vocabulary into a payload the kernel would
// then have to understand.
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

func validateSenseRequest(request SenseRequest) error {
	if !request.EffectID.Valid() || !request.Input.Valid() {
		return errors.New("planning: invalid sensing request")
	}
	return nil
}

func validateActionRequest(request ActionRequest) error {
	if !request.EffectID.Valid() || !request.Input.Valid() || !validName(request.ActionName) ||
		!validDescription(request.ActionDescription) || !request.WorldState.Valid() {
		return fmt.Errorf("planning: invalid Action request for %q", request.ActionName)
	}
	return nil
}
