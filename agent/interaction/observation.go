package interaction

import (
	jsonv2 "encoding/json/v2"
	"fmt"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

const (
	failureCodeDispatchRejected = "interaction.dispatch.rejected"
	failureCodeModelFailed      = "interaction.model.failed"
	failureCodeModelNotExecuted = "interaction.model.not_executed"
)

type modelUncertainty struct {
	Diagnostic string         `json:"diagnostic"`
	Response   *chat.Response `json:"response,omitzero"`
}

func (m modelUncertainty) validate() error {
	if !run.ValidDiagnostic(m.Diagnostic) {
		return ErrInvalidProtocol
	}
	if m.Response != nil {
		return m.Response.Validate()
	}
	return nil
}

type toolUncertainty struct {
	Diagnostic string       `json:"diagnostic"`
	Evidence   *tool.Output `json:"evidence,omitzero"`
}

func (t toolUncertainty) validate() error {
	if !run.ValidDiagnostic(t.Diagnostic) {
		return ErrInvalidProtocol
	}
	if t.Evidence != nil {
		return t.Evidence.Validate()
	}
	return nil
}

type dispatchUncertainty struct {
	Model *modelUncertainty `json:"model,omitzero"`
	Tool  *toolUncertainty  `json:"tool,omitzero"`
}

func (d dispatchUncertainty) diagnostic() (string, error) {
	if (d.Model == nil) == (d.Tool == nil) {
		return "", ErrInvalidProtocol
	}
	if d.Model != nil {
		return d.Model.Diagnostic, d.Model.validate()
	}
	return d.Tool.Diagnostic, d.Tool.validate()
}

func (d dispatchUncertainty) modelSettlement() (ModelSettlement, error) {
	diagnostic, err := d.diagnostic()
	if err != nil {
		return ModelSettlement{}, err
	}
	if d.Model == nil {
		return ModelSettlement{}, ErrInvalidProtocol
	}
	result := ModelSettlement{Outcome: ModelOutcomeUnknown, Failure: diagnostic, Response: d.Model.Response.Clone()}
	return result, result.Validate()
}

func (d dispatchUncertainty) toolSettlement() (ToolSettlement, error) {
	diagnostic, err := d.diagnostic()
	if err != nil {
		return ToolSettlement{}, err
	}
	if d.Tool == nil {
		return ToolSettlement{}, ErrInvalidProtocol
	}
	result := ToolSettlement{Outcome: ToolOutcomeUnknown, Failure: diagnostic}
	if d.Tool.Evidence != nil {
		result.Evidence = new(d.Tool.Evidence.Clone())
	}
	return result, nil
}

func (d dispatchUncertainty) settlement() (run.Settlement, error) {
	if _, err := d.diagnostic(); err != nil {
		return run.Settlement{}, err
	}
	encoded, err := jsonv2.Marshal(d)
	if err != nil {
		return run.Settlement{}, err
	}
	return run.NewSettlement(run.SettlementStatusUnknown, encoded)
}

// A failed model call can still own completed generation and usage. The run
// envelope owns its diagnostic and outcome; these details retain only evidence.
type failedModelDetails struct {
	Response *chat.Response `json:"response"`
}

func (f failedModelDetails) validate() error {
	if f.Response == nil {
		return ErrInvalidProtocol
	}
	return f.Response.Validate()
}

func ModelRequestFromEffect(effect run.Effect) (*chat.Request, bool, error) {
	if effect.Target() != run.EffectTargetDispatcher {
		return nil, false, nil
	}
	envelope, err := decodeEffect(effect.Payload().JSON())
	if err != nil {
		return nil, false, err
	}
	if envelope.operation() != operationModelCall {
		return nil, false, nil
	}
	return envelope.ModelCall.request(), true, nil
}

// ToolCallIntent is the call a committed tool Effect dispatches.
// ContinuationOf is the Effect of the original call when this Effect resumes it
// with requested input, and zero for the original dispatch.
type ToolCallIntent struct {
	Call           chat.ToolCall
	ContinuationOf run.EffectID
}

func ToolCallFromEffect(effect run.Effect) (ToolCallIntent, bool, error) {
	if effect.Target() != run.EffectTargetDispatcher {
		return ToolCallIntent{}, false, nil
	}
	envelope, err := decodeEffect(effect.Payload().JSON())
	if err != nil {
		return ToolCallIntent{}, false, err
	}
	if envelope.operation() != operationToolCall {
		return ToolCallIntent{}, false, nil
	}
	intent := ToolCallIntent{Call: envelope.ToolCall.Invocation.Call}
	if envelope.ToolCall.Resume != nil {
		intent.ContinuationOf = envelope.ToolCall.Resume.Reference
	}
	return intent, true, nil
}
func ModelSettlementFromOutcome(outcome run.Settlement) (ModelSettlement, error) {
	if !outcome.Valid() {
		return ModelSettlement{}, ErrInvalidProtocol
	}
	switch outcome.Status() {
	case run.SettlementStatusUnknown:
		evidence, err := outcome.Payload().Decode[dispatchUncertainty]()
		if err != nil {
			return ModelSettlement{}, err
		}
		return evidence.modelSettlement()
	case run.SettlementStatusFailed:
		failure, _ := outcome.Failure()
		result := ModelSettlement{Outcome: ModelOutcomeFailed, Failure: failure.Message()}
		if failure.Code() == failureCodeModelNotExecuted {
			result.Outcome = ModelOutcomeNotExecuted
		}
		if outcome.Payload().Valid() {
			if failure.Code() != failureCodeModelFailed {
				return ModelSettlement{}, ErrInvalidProtocol
			}
			details, err := outcome.Payload().Decode[failedModelDetails]()
			if err != nil {
				return ModelSettlement{}, err
			}
			if err := details.validate(); err != nil {
				return ModelSettlement{}, err
			}
			result.Response = details.Response.Clone()
		}
		return result, result.Validate()
	default:
		envelope, err := decodeResult(outcome.Payload().JSON())
		if err != nil {
			return ModelSettlement{}, err
		}
		if envelope.ModelResult == nil {
			return ModelSettlement{}, ErrInvalidProtocol
		}
		return ModelSettlement{Outcome: ModelOutcomeSucceeded, Response: envelope.ModelResult.Response.Clone()}, nil
	}
}
func ToolSettlementFromOutcome(call chat.ToolCall, outcome run.Settlement) (ToolSettlement, error) {
	if !outcome.Valid() || call.Validate() != nil {
		return ToolSettlement{}, ErrInvalidProtocol
	}
	if outcome.Status() == run.SettlementStatusFailed {
		failure, _ := outcome.Failure()
		if outcome.Payload().Valid() || failure.Code() == failureCodeModelFailed || failure.Code() == failureCodeModelNotExecuted {
			return ToolSettlement{}, ErrInvalidProtocol
		}
		return ToolSettlement{Outcome: ToolOutcomeDispatchFailed, Failure: failure.Message()}, nil
	}
	if outcome.Status() == run.SettlementStatusUnknown {
		evidence, err := outcome.Payload().Decode[dispatchUncertainty]()
		if err != nil {
			return ToolSettlement{}, err
		}
		return evidence.toolSettlement()
	}

	envelope, err := decodeResult(outcome.Payload().JSON())
	if err != nil {
		return ToolSettlement{}, err
	}
	if envelope.ToolResult == nil {
		return ToolSettlement{}, ErrInvalidProtocol
	}
	if envelope.ToolResult.InputRequest != nil {
		return ToolSettlement{Outcome: ToolOutcomeInputRequired}, nil
	}
	if err := envelope.ToolResult.Completion.validateCall(call); err != nil {
		return ToolSettlement{}, fmt.Errorf("%w: tool result: %w", ErrInvalidProtocol, err)
	}
	completion := envelope.ToolResult.Completion
	return ToolSettlement{Outcome: ToolOutcome(completion.Disposition), Result: new(completion.toolResult(call))}, nil
}
