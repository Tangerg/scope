package interaction

import (
	jsonv2 "encoding/json/v2"
	"fmt"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

type ToolSettlement struct {
	Result        *chat.ToolResult `json:"result,omitzero"`
	InputRequired bool             `json:"input_required,omitzero"`
	Failure       string           `json:"failure,omitzero"`
	Evidence      *tool.Output     `json:"evidence,omitzero"`
}

// The outer Settlement owns success, failure, and uncertainty. These payloads
// add evidence without retaining another writable outcome discriminator.
type modelFailure struct {
	Diagnostic  string         `json:"diagnostic"`
	Response    *chat.Response `json:"response,omitzero"`
	NotExecuted bool           `json:"not_executed,omitzero"`
}

func (m modelFailure) validate() error {
	if !run.ValidDiagnostic(m.Diagnostic) || m.NotExecuted && m.Response != nil {
		return ErrInvalidProtocol
	}
	if m.Response != nil {
		return m.Response.Validate()
	}
	return nil
}

type toolFailure struct {
	Diagnostic string       `json:"diagnostic"`
	Evidence   *tool.Output `json:"evidence,omitzero"`
}

func (t toolFailure) validate() error {
	if !run.ValidDiagnostic(t.Diagnostic) {
		return ErrInvalidProtocol
	}
	if t.Evidence != nil {
		return t.Evidence.Validate()
	}
	return nil
}

type dispatchFailure struct {
	Diagnostic string        `json:"diagnostic,omitzero"`
	Model      *modelFailure `json:"model,omitzero"`
	Tool       *toolFailure  `json:"tool,omitzero"`
}

func (d dispatchFailure) diagnostic(status run.SettlementStatus) (string, error) {
	if status != run.SettlementStatusFailed && status != run.SettlementStatusUnknown ||
		d.Model != nil && d.Model.NotExecuted && status != run.SettlementStatusFailed ||
		d.Tool != nil && status != run.SettlementStatusUnknown {
		return "", ErrInvalidProtocol
	}

	count := 0
	diagnostic := d.Diagnostic
	if diagnostic != "" {
		count++
	}
	if d.Model != nil {
		count++
		if err := d.Model.validate(); err != nil {
			return "", err
		}
		diagnostic = d.Model.Diagnostic
	}
	if d.Tool != nil {
		count++
		if err := d.Tool.validate(); err != nil {
			return "", err
		}
		diagnostic = d.Tool.Diagnostic
	}
	if count != 1 || !run.ValidDiagnostic(diagnostic) {
		return "", ErrInvalidProtocol
	}
	return diagnostic, nil
}

// modelSettlement reads the evidence of a model call that did not succeed. A
// response recorded beside the failure survives as evidence, never as success.
func (d dispatchFailure) modelSettlement(status run.SettlementStatus) (ModelSettlement, error) {
	diagnostic, err := d.diagnostic(status)
	if err != nil {
		return ModelSettlement{}, err
	}
	if d.Tool != nil {
		return ModelSettlement{}, ErrInvalidProtocol
	}
	result := ModelSettlement{Outcome: ModelOutcomeFailed, Failure: diagnostic}
	if status == run.SettlementStatusUnknown {
		result.Outcome = ModelOutcomeUnknown
	}
	if d.Model != nil {
		if d.Model.NotExecuted {
			result.Outcome = ModelOutcomeNotExecuted
		}
		result.Response = d.Model.Response.Clone()
	}
	return result, result.Validate()
}

func (d dispatchFailure) toolSettlement(status run.SettlementStatus) (ToolSettlement, error) {
	diagnostic, err := d.diagnostic(status)
	if err != nil {
		return ToolSettlement{}, err
	}
	if d.Model != nil {
		return ToolSettlement{}, ErrInvalidProtocol
	}
	result := ToolSettlement{Failure: diagnostic}
	if d.Tool != nil && d.Tool.Evidence != nil {
		result.Evidence = new(d.Tool.Evidence.Clone())
	}
	return result, nil
}

func (d dispatchFailure) settlement(status run.SettlementStatus) (run.Settlement, error) {
	if _, err := d.diagnostic(status); err != nil {
		return run.Settlement{}, err
	}
	encoded, err := jsonv2.Marshal(d)
	if err != nil {
		return run.Settlement{}, err
	}
	return run.NewSettlement(status, encoded)
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
func ToolCallFromEffect(effect run.Effect) (chat.ToolCall, run.EffectID, bool, error) {
	if effect.Target() != run.EffectTargetDispatcher {
		return chat.ToolCall{}, run.EffectID{}, false, nil
	}
	envelope, err := decodeEffect(effect.Payload().JSON())
	if err != nil {
		return chat.ToolCall{}, run.EffectID{}, false, err
	}
	if envelope.operation() != operationToolCall {
		return chat.ToolCall{}, run.EffectID{}, false, nil
	}
	var reference run.EffectID
	if envelope.ToolCall.Resume != nil {
		reference = envelope.ToolCall.Resume.Reference
	}
	return envelope.ToolCall.Invocation.Call, reference, true, nil
}
func ModelSettlementFromOutcome(outcome run.Settlement) (ModelSettlement, error) {
	if !outcome.Valid() {
		return ModelSettlement{}, ErrInvalidProtocol
	}
	if outcome.Status() != run.SettlementStatusSucceeded {
		failure, err := outcome.Payload().Decode[dispatchFailure]()
		if err != nil {
			return ModelSettlement{}, err
		}
		return failure.modelSettlement(outcome.Status())
	}
	envelope, err := decodeResult(outcome.Payload().JSON())
	if err != nil {
		return ModelSettlement{}, err
	}
	if envelope.ModelResult == nil {
		return ModelSettlement{}, ErrInvalidProtocol
	}
	return ModelSettlement{Outcome: ModelOutcomeSucceeded, Response: envelope.ModelResult.Response.Clone()}, nil
}
func ToolSettlementFromOutcome(call chat.ToolCall, outcome run.Settlement) (ToolSettlement, error) {
	if !outcome.Valid() || call.Validate() != nil {
		return ToolSettlement{}, ErrInvalidProtocol
	}
	if outcome.Status() != run.SettlementStatusSucceeded {
		failure, err := outcome.Payload().Decode[dispatchFailure]()
		if err != nil {
			return ToolSettlement{}, err
		}
		return failure.toolSettlement(outcome.Status())
	}
	envelope, err := decodeResult(outcome.Payload().JSON())
	if err != nil {
		return ToolSettlement{}, err
	}
	if envelope.ToolResult == nil {
		return ToolSettlement{}, ErrInvalidProtocol
	}
	if envelope.ToolResult.InputRequest != nil {
		return ToolSettlement{InputRequired: true}, nil
	}
	if err := envelope.ToolResult.Completion.validateCall(call); err != nil {
		return ToolSettlement{}, fmt.Errorf("%w: tool result: %w", ErrInvalidProtocol, err)
	}
	result := envelope.ToolResult.Completion.toolResult(call)
	return ToolSettlement{Result: &result}, nil
}
