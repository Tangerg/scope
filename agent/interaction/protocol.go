package interaction

import (
	"encoding/json"
	"fmt"

	"github.com/Tangerg/scope/agent/internal/strategy/stepfail"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

type operation string

const (
	operationPrepare   operation = "prepare_context"
	operationModelCall operation = "model_call"
	operationToolCall  operation = "tool_call"
)

// effectEnvelope carries exactly one request; the member present names its
// operation.
type effectEnvelope struct {
	Preparation *modelCall           `json:"prepare_context,omitzero"`
	ModelCall   *modelCall           `json:"model_call,omitzero"`
	ToolCall    *toolDispatchRequest `json:"tool_call,omitzero"`
}

func (e effectEnvelope) operation() operation {
	switch {
	case e.Preparation != nil:
		return operationPrepare
	case e.ModelCall != nil:
		return operationModelCall
	case e.ToolCall != nil:
		return operationToolCall
	default:
		return ""
	}
}

type modelCall struct {
	ModelCallSequence     uint64            `json:"model_call_sequence"`
	Request               Input             `json:"request"`
	Tools                 []tool.Definition `json:"tools,omitempty"`
	AppliedSteerSignalIDs []run.SignalID    `json:"applied_steer_signal_ids,omitempty"`
}

func (m modelCall) request() *chat.Request {
	request := m.Request.request()
	request.Tools = cloneToolDefinitions(m.Tools)
	return request
}

// validate checks the request the model will receive: Input alone cannot see
// how its options bind to the advertised Tools.
func (m modelCall) validate() error {
	if m.ModelCallSequence == 0 {
		return fmt.Errorf("%w: model call sequence is zero", ErrInvalidProtocol)
	}
	request := chat.Request{Messages: m.Request.Messages, Options: m.Request.Options, Tools: m.Tools}
	if err := request.Validate(); err != nil {
		return fmt.Errorf("%w: model call request: %w", ErrInvalidProtocol, err)
	}
	if len(m.AppliedSteerSignalIDs) > 0 {
		if err := validateSteerSignalIDs(m.AppliedSteerSignalIDs); err != nil {
			return fmt.Errorf("%w: model call applied steer SignalIDs: %w", ErrInvalidProtocol, err)
		}
	}
	return nil
}

// The model owns call ID and name; the EffectID owns execution correlation.
type toolCall struct {
	Call chat.ToolCall `json:"call"`
}

func (t toolCall) validate() error {
	if err := t.Call.Validate(); err != nil {
		return fmt.Errorf("%w: tool_call: %w", ErrInvalidProtocol, err)
	}
	return nil
}

type toolDispatchRequest struct {
	Invocation toolCall    `json:"invocation"`
	Resume     *toolResume `json:"resume,omitzero"`
}

func (t toolDispatchRequest) validate() error {
	if err := t.Invocation.validate(); err != nil {
		return err
	}
	if t.Resume == nil {
		return nil
	}
	if !t.Resume.Reference.Valid() || !t.Resume.InputRequest.Valid() {
		return fmt.Errorf("%w: tool resume input: %w", ErrInvalidProtocol, tool.ErrInvalidInputRequest)
	}
	if _, err := t.Resume.InputRequest.Continuation(t.Resume.InputResponse); err != nil {
		return fmt.Errorf("%w: tool resume input: %w", ErrInvalidProtocol, err)
	}
	return nil
}

func (t toolDispatchRequest) effect() (run.Effect, error) {
	return effectEnvelope{ToolCall: &t}.effect()
}

// toolResume continues the Tool with the input it requested, carrying the
// originating request and the response delivered to it.
type toolResume struct {
	InputRequest  tool.InputRequest `json:"input_request"`
	Reference     run.EffectID      `json:"reference"`
	InputResponse json.RawMessage   `json:"input_response"`
}

func (t toolResume) precedes(effect run.EffectID) bool {
	return t.Reference.Valid() && effect.Valid() && t.Reference.Process == effect.Process && t.Reference.Step < effect.Step
}

// Dispatcher results exclude external guidance: only a settled Effect may
// advance the operation that emitted it.
type resultEnvelope struct {
	Prepared    *preparedContext    `json:"prepared_context,omitzero"`
	ModelResult *modelCallResult    `json:"model_result,omitzero"`
	ToolResult  *toolDispatchResult `json:"tool_result,omitzero"`
}

// preparedContext can replace messages without changing the already bound
// tools or request options.
type preparedContext struct {
	Messages []chat.Message `json:"messages"`
}

func (p preparedContext) validate() error {
	if len(p.Messages) == 0 {
		return fmt.Errorf("%w: prepared messages are empty", ErrInvalidProtocol)
	}
	if err := (Input{Messages: p.Messages}).Validate(); err != nil {
		return fmt.Errorf("%w: prepared context: %w", ErrInvalidProtocol, err)
	}
	return nil
}

// The outer Settlement owns success or failure; this payload owns only the
// completed generation.
type modelCallResult struct {
	Response *chat.Response `json:"response"`
}

func (m modelCallResult) validate() error {
	if m.Response == nil {
		return fmt.Errorf("%w: model_result requires a response", ErrInvalidProtocol)
	}
	if err := m.Response.Validate(); err != nil {
		return fmt.Errorf("%w: model_result response: %w", ErrInvalidProtocol, err)
	}

	return nil
}

func (m modelCallResult) settlement() (run.Settlement, error) {
	return resultEnvelope{ModelResult: &m}.settlement()
}

// settlement encodes a definite successful dispatcher result; every success
// the Interaction protocol records is one of these envelopes.
func (r resultEnvelope) settlement() (run.Settlement, error) {
	if err := r.validate(); err != nil {
		return run.Settlement{}, err
	}
	payload, err := run.EncodePayload(r)
	if err != nil {
		return run.Settlement{}, err
	}
	return run.NewSettlement(run.SettlementStatusSucceeded, payload)
}

type toolDispatchResult struct {
	Completion   *toolCallResult    `json:"completion,omitzero"`
	InputRequest *tool.InputRequest `json:"input_request,omitzero"`
	// The external call completed before this local projection failed. Keeping
	// both facts lets Step fail without inventing external uncertainty.
	AdvertisementFailure string `json:"advertisement_failure,omitzero"`
}

func (e effectEnvelope) validate() error {
	switch {
	case e.Preparation != nil && e.ModelCall == nil && e.ToolCall == nil:
		return e.Preparation.validate()
	case e.ModelCall != nil && e.Preparation == nil && e.ToolCall == nil:
		return e.ModelCall.validate()
	case e.ToolCall != nil && e.Preparation == nil && e.ModelCall == nil:
		return e.ToolCall.validate()
	default:
		return fmt.Errorf("%w: effect requires exactly one operation request", ErrInvalidProtocol)
	}
}

// effect admits an envelope by the rule set the decode path also enforces, so
// the Execution can never emit an Effect its Dispatcher would reject.
func (e effectEnvelope) effect() (run.Effect, error) {
	if err := e.validate(); err != nil {
		return run.Effect{}, err
	}
	payload, err := run.EncodePayload(e)
	if err != nil {
		return run.Effect{}, err
	}
	return run.NewDispatcherEffect(payload)
}

func (r resultEnvelope) operation() operation {
	switch {
	case r.Prepared != nil:
		return operationPrepare
	case r.ModelResult != nil:
		return operationModelCall
	case r.ToolResult != nil:
		return operationToolCall
	default:
		return ""
	}
}

func (r resultEnvelope) validate() error {
	switch {
	case r.Prepared != nil && r.ModelResult == nil && r.ToolResult == nil:
		return r.Prepared.validate()
	case r.ModelResult != nil && r.Prepared == nil && r.ToolResult == nil:
		return r.ModelResult.validate()
	case r.ToolResult != nil && r.Prepared == nil && r.ModelResult == nil:
		return r.ToolResult.validate()
	default:
		return fmt.Errorf("%w: result requires exactly one operation outcome", ErrInvalidProtocol)
	}
}

func (t toolDispatchResult) settlement() (run.Settlement, error) {
	return resultEnvelope{ToolResult: &t}.settlement()
}

func (t toolDispatchResult) validate() error {
	if (t.Completion == nil) == (t.InputRequest == nil) {
		return fmt.Errorf("%w: Tool dispatch requires one result or input request", ErrInvalidProtocol)
	}
	if t.AdvertisementFailure != "" && (!run.ValidDiagnostic(t.AdvertisementFailure) ||
		t.Completion == nil || t.Completion.Disposition != ResultSucceeded || len(t.Completion.AdvertisedToolNames) != 0) {
		return fmt.Errorf("%w: advertisement failure requires a completed Tool and no declaration", ErrInvalidProtocol)
	}
	if t.InputRequest != nil {
		if !t.InputRequest.Valid() {
			return fmt.Errorf("%w: tool input: %w", ErrInvalidProtocol, tool.ErrInvalidInputRequest)
		}
		return nil
	}
	return t.Completion.validate()
}

func decodeEffect(payload run.Payload) (effectEnvelope, error) {
	return decodeEnvelope[effectEnvelope](payload, "effect")
}

// decodeEnvelope admits one protocol envelope: strict decoding and then the
// envelope's own validation, which already classifies its violations.
func decodeEnvelope[E interface{ validate() error }](payload run.Payload, role string) (E, error) {
	var zero E
	envelope, err := payload.Decode[E]()
	if err != nil {
		return zero, fmt.Errorf("%w: decode %s: %w", ErrInvalidProtocol, role, err)
	}
	if err := envelope.validate(); err != nil {
		return zero, err
	}
	return envelope, nil
}

// failedSettlement settles a dispatcher Effect the Interaction protocol
// itself rejects: a malformed Effect or an unconfigured collaborator.
func failedSettlement(cause error) (run.Settlement, error) {
	return stepfail.Settlement(run.FailureKindContract, failureCodeDispatchRejected, cause.Error())
}

// decodeSettlement reads the definite settlement delivered for an Effect of
// the expected operation. A failure keeps the classification its dispatcher
// recorded, so the Step that ends on it terminates with that same Failure.
func decodeSettlement(settlement run.Settlement, expected operation) (resultEnvelope, run.Failure, error) {
	if !settlement.Valid() || settlement.Status() == run.SettlementStatusUnknown {
		return resultEnvelope{}, run.Failure{}, ErrInvalidProtocol
	}
	if failure, failed := settlement.Failure(); failed {
		_, err := expected.failedResponse(settlement)
		return resultEnvelope{}, failure, err
	}
	envelope, err := decodeResult(settlement.Payload())
	if err == nil && envelope.operation() != expected {
		err = ErrInvalidProtocol
	}
	return envelope, run.Failure{}, err
}

// failedResponse admits a failed settlement of an Effect of this operation and
// returns the generation a failed model call completed, if any. Only a model
// call carries model classifications or failure details; Step decoding and the
// public projections apply this one rule.
func (o operation) failedResponse(outcome run.Settlement) (*chat.Response, error) {
	failure, _ := outcome.Failure()
	if o != operationModelCall {
		if outcome.Payload().Valid() || failure.Code() == failureCodeModelFailed || failure.Code() == failureCodeModelNotExecuted {
			return nil, ErrInvalidProtocol
		}
		return nil, nil
	}
	if !outcome.Payload().Valid() {
		return nil, nil
	}
	if failure.Code() != failureCodeModelFailed {
		return nil, ErrInvalidProtocol
	}
	details, err := outcome.Payload().Decode[failedModelDetails]()
	if err != nil {
		return nil, err
	}
	if err := details.validate(); err != nil {
		return nil, err
	}
	return details.Response, nil
}

func decodeResult(payload run.Payload) (resultEnvelope, error) {
	return decodeEnvelope[resultEnvelope](payload, "result")
}
