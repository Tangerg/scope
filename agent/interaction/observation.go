package interaction

import (
	"errors"
	"fmt"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

const (
	failureCodeDispatchRejected = "interaction.dispatch.rejected"
	failureCodeContextRejected  = "interaction.context.rejected"
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
	payload, err := run.EncodePayload(d)
	if err != nil {
		return run.Settlement{}, err
	}
	return run.NewSettlement(run.SettlementStatusUnknown, payload)
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

// Sequence comes from the committed intent; observers must not reconstruct it
// from a filtered history or use a presentation ordinal as a child identity.
type ModelCallIntent struct {
	Sequence uint64
	Request  *chat.Request
}

func ModelCallFromEffect(effect run.Effect) (ModelCallIntent, bool, error) {
	if effect.Target() != run.EffectTargetDispatcher {
		return ModelCallIntent{}, false, nil
	}
	envelope, err := decodeEffect(effect.Payload().JSON())
	if err != nil {
		return ModelCallIntent{}, false, err
	}
	if envelope.operation() != operationModelCall {
		return ModelCallIntent{}, false, nil
	}
	return ModelCallIntent{Sequence: envelope.ModelCall.ModelCallSequence, Request: envelope.ModelCall.request()}, true, nil
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

// ModelSettlementFromRecord projects the committed settlement of a model call,
// including one the runtime left Unknown because Dispatch returned an error.
func ModelSettlementFromRecord(record run.EffectSettled) (ModelSettlement, error) {
	if diagnostic, recorded, err := runtimeUncertainty(record); recorded || err != nil {
		result := ModelSettlement{Outcome: ModelOutcomeUnknown, Failure: diagnostic}
		return result, errors.Join(err, result.Validate())
	}
	outcome := record.Outcome
	switch outcome.Status() {
	case run.SettlementStatusUnknown:
		evidence, err := outcome.Payload().Decode[dispatchUncertainty]()
		if err != nil {
			return ModelSettlement{}, err
		}
		return evidence.modelSettlement()
	case run.SettlementStatusFailed:
		response, err := operationModelCall.failedResponse(outcome)
		if err != nil {
			return ModelSettlement{}, err
		}
		failure, _ := outcome.Failure()
		result := ModelSettlement{Outcome: ModelOutcomeFailed, Failure: failure.Message(), Response: response}
		if failure.Code() == failureCodeModelNotExecuted {
			result.Outcome = ModelOutcomeNotExecuted
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

// ToolSettlementFromRecord projects the committed settlement of call,
// including one the runtime left Unknown because Dispatch returned an error.
func ToolSettlementFromRecord(call chat.ToolCall, record run.EffectSettled) (ToolSettlement, error) {
	if call.Validate() != nil {
		return ToolSettlement{}, ErrInvalidProtocol
	}
	if diagnostic, recorded, err := runtimeUncertainty(record); recorded || err != nil {
		return ToolSettlement{Outcome: ToolOutcomeUnknown, Failure: diagnostic}, err
	}
	outcome := record.Outcome
	if failure, failed := outcome.Failure(); failed {
		if _, err := operationToolCall.failedResponse(outcome); err != nil {
			return ToolSettlement{}, err
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

// runtimeUncertainty reads the diagnostic of a settlement the runtime itself
// recorded as Unknown: Dispatch returned an error, so the record carries a
// runtime Failure and a null outcome payload instead of dispatcher evidence.
func runtimeUncertainty(record run.EffectSettled) (string, bool, error) {
	if !record.Outcome.Valid() {
		return "", false, ErrInvalidProtocol
	}
	if record.Failure == nil {
		return "", false, nil
	}
	if record.Outcome.Status() != run.SettlementStatusUnknown || !record.Failure.Valid() {
		return "", false, ErrInvalidProtocol
	}
	return record.Failure.Message(), true, nil
}
