package interaction

import (
	"fmt"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
)

// ModelOutcome belongs to the complete model invocation, including middleware.
// Failed does not establish token use.
type ModelOutcome string

const (
	ModelOutcomeInvalid     ModelOutcome = ""
	ModelOutcomeSucceeded   ModelOutcome = "succeeded"
	ModelOutcomeFailed      ModelOutcome = "failed"
	ModelOutcomeNotExecuted ModelOutcome = "not_executed"
	ModelOutcomeUnknown     ModelOutcome = "unknown"
)

func (m ModelOutcome) Valid() bool {
	switch m {
	case ModelOutcomeSucceeded, ModelOutcomeFailed, ModelOutcomeNotExecuted, ModelOutcomeUnknown:
		return true
	default:
		return false
	}
}

// ModelSettlement is a detached projection of the committed Effect settlement.
// Response retains generation even when postprocessing failed or is uncertain;
// its presence cannot prove middleware success. Only NotExecuted establishes
// that no provider work ran.
type ModelSettlement struct {
	Outcome  ModelOutcome   `json:"outcome"`
	Response *chat.Response `json:"response,omitzero"`
	Failure  string         `json:"failure,omitzero"`
}

func (m ModelSettlement) Validate() error {
	if m.Failure != "" && !run.ValidDiagnostic(m.Failure) {
		return fmt.Errorf("%w: failure must be a bounded diagnostic", ErrInvalidModelSettlement)
	}
	if m.Response != nil {
		if err := m.Response.Validate(); err != nil {
			return fmt.Errorf("%w: response: %w", ErrInvalidModelSettlement, err)
		}
	}
	switch m.Outcome {
	case ModelOutcomeSucceeded:
		if m.Response == nil || m.Failure != "" {
			return fmt.Errorf("%w: success requires only a response", ErrInvalidModelSettlement)
		}
	case ModelOutcomeNotExecuted:
		if m.Response != nil || m.Failure == "" {
			return fmt.Errorf("%w: non-execution requires only a diagnostic", ErrInvalidModelSettlement)
		}
	case ModelOutcomeFailed:
		if m.Failure == "" {
			return fmt.Errorf("%w: failed invocation requires a diagnostic", ErrInvalidModelSettlement)
		}
	case ModelOutcomeUnknown:
		if m.Response != nil && m.Failure == "" {
			return fmt.Errorf("%w: known response with unknown invocation requires a diagnostic", ErrInvalidModelSettlement)
		}
	default:
		return fmt.Errorf("%w: a settlement must establish an observed outcome", ErrInvalidModelSettlement)
	}
	return nil
}

func modelFailureSettlement(response *chat.Response, cause error) (run.Settlement, error) {
	if cause == nil {
		return run.Settlement{}, ErrInvalidModelSettlement
	}
	failure := modelFailure{Diagnostic: run.NormalizeDiagnostic(cause.Error()), Response: response}
	status := run.SettlementStatusUnknown
	switch chat.ClassifyFailure(cause) {
	case chat.CallFailureNotExecuted:
		failure.NotExecuted = true
		status = run.SettlementStatusFailed
	case chat.CallFailureFailed:
		status = run.SettlementStatusFailed
	}
	return (dispatchFailure{Model: &failure}).settlement(status)
}
