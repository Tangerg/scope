package interaction

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
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

func (m modelCallResult) settlement(maxBytes int) (run.Settlement, error) {
	if err := m.validate(); err != nil {
		return run.Settlement{}, err
	}
	payload, err := run.EncodePayload(resultEnvelope{ModelResult: &m})
	if err != nil {
		return run.Settlement{}, err
	}
	if len(payload.JSON()) > maxBytes {
		return run.Settlement{}, ErrModelResponseTooLarge
	}
	return run.NewSettlement(run.SettlementStatusSucceeded, payload.JSON())
}

// toolCallResult is what executing one ToolCall adds: its output, disposition,
// and advertisements. The call owns the ID and name that the model-facing
// chat.ToolResult carries, and the frozen ToolSet owns whether a successful
// result completes directly, so neither is stored here.
type toolCallResult struct {
	Disposition         ResultDisposition `json:"disposition"`
	Output              tool.Output       `json:"output"`
	AdvertisedToolNames []string          `json:"advertised_tool_names,omitempty"`
}

// newToolCallResult keeps what a model-facing result adds to the call it answers.
func newToolCallResult(result chat.ToolResult) toolCallResult {
	disposition := ResultSucceeded
	if result.IsError {
		disposition = ResultFailed
	}
	return toolCallResult{Disposition: disposition, Output: result.Output.Clone()}
}

// toolResult renders the result the model sees for call.
func (t toolCallResult) toolResult(call chat.ToolCall) chat.ToolResult {
	return chat.ToolResult{ID: call.ID, Name: call.Name, Output: t.Output.Clone(), IsError: t.Disposition != ResultSucceeded}
}

type toolDispatchResult struct {
	Completion   *toolCallResult    `json:"completion,omitzero"`
	InputRequest *tool.InputRequest `json:"input_request,omitzero"`
	// The external call completed before this local projection failed. Keeping
	// both facts lets Step fail without inventing external uncertainty.
	AdvertisementFailure string `json:"advertisement_failure,omitzero"`
}

func newModelEffect(
	request *Input,
	modelCallSequence uint64,
	tools []tool.Definition,
	appliedSteerSignalIDs []run.SignalID,
) (effectEnvelope, error) {
	if request == nil {
		return effectEnvelope{}, fmt.Errorf("%w: model request is nil", ErrInvalidProtocol)
	}
	// validateModelCall owns the rule set the decode path also enforces.
	envelope := effectEnvelope{
		ModelCall: &modelCall{
			ModelCallSequence:     modelCallSequence,
			Request:               *request.clone(),
			Tools:                 cloneToolDefinitions(tools),
			AppliedSteerSignalIDs: slices.Clone(appliedSteerSignalIDs),
		},
	}
	if err := envelope.validateModelCall(); err != nil {
		return effectEnvelope{}, err
	}
	return envelope, nil
}

func newToolEffect(call toolDispatchRequest) (effectEnvelope, error) {
	envelope := effectEnvelope{ToolCall: &call}
	if err := envelope.validateToolCall(); err != nil {
		return effectEnvelope{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	return envelope, nil
}

func (e effectEnvelope) validate() error {
	switch e.operation() {
	case operationPrepare:
		if e.ToolCall != nil || e.ModelCall != nil {
			return ErrInvalidProtocol
		}
		return (effectEnvelope{ModelCall: e.Preparation}).validateModelCall()
	case operationModelCall:
		return e.validateModelCall()
	case operationToolCall:
		return e.validateToolCall()
	default:
		return fmt.Errorf("%w: unsupported effect protocol", ErrInvalidProtocol)
	}
}

func (e effectEnvelope) validateModelCall() error {
	if e.ModelCall == nil || e.ToolCall != nil || e.ModelCall.ModelCallSequence == 0 {
		return fmt.Errorf("%w: model_call effect has an invalid payload set", ErrInvalidProtocol)
	}
	if err := e.ModelCall.request().Validate(); err != nil {
		return fmt.Errorf("%w: model_call request: %w", ErrInvalidProtocol, err)
	}
	if len(e.ModelCall.AppliedSteerSignalIDs) > 0 {
		if err := validateSteerSignalIDs(e.ModelCall.AppliedSteerSignalIDs); err != nil {
			return fmt.Errorf("%w: model_call applied steer SignalIDs: %w", ErrInvalidProtocol, err)
		}
	}
	return nil
}

func (e effectEnvelope) validateToolCall() error {
	if e.ModelCall != nil || e.ToolCall == nil {
		return fmt.Errorf("%w: tool_call effect has an invalid payload set", ErrInvalidProtocol)
	}
	if err := e.ToolCall.Invocation.validate(); err != nil {
		return err
	}
	resume := e.ToolCall.Resume
	if resume == nil {
		return nil
	}
	if !resume.Reference.Valid() || !resume.InputRequest.Valid() {
		return fmt.Errorf("%w: tool resume input: %w", ErrInvalidProtocol, tool.ErrInvalidInputRequest)
	}
	_, err := resume.InputRequest.Continuation(resume.InputResponse)
	return err
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
	if err := t.validate(); err != nil {
		return run.Settlement{}, err
	}
	payload, err := run.EncodePayload(resultEnvelope{ToolResult: &t})
	if err != nil {
		return run.Settlement{}, err
	}
	return run.NewSettlement(run.SettlementStatusSucceeded, payload.JSON())
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

func (t toolCallResult) clone() toolCallResult {
	t.Output = t.Output.Clone()
	t.AdvertisedToolNames = slices.Clone(t.AdvertisedToolNames)
	return t
}

func (t toolCallResult) validate() error {
	if !t.Disposition.Valid() {
		return fmt.Errorf("%w: tool_result disposition is invalid", ErrInvalidProtocol)
	}
	if err := t.Output.Validate(); err != nil {
		return fmt.Errorf("%w: tool_result: %w", ErrInvalidProtocol, err)
	}
	if t.Disposition != ResultSucceeded && len(t.AdvertisedToolNames) != 0 {
		return fmt.Errorf("%w: failed tool_result cannot advertise Tools", ErrInvalidProtocol)
	}
	if err := validateAdvertisedToolNames(t.AdvertisedToolNames); err != nil {
		return fmt.Errorf("%w: tool_result advertised Tools: %w", ErrInvalidProtocol, err)
	}
	return nil
}

func (t toolCallResult) validateCall(call chat.ToolCall) error {
	if err := t.validate(); err != nil {
		return err
	}
	if err := t.toolResult(call).Validate(); err != nil {
		return fmt.Errorf("%w: tool_result: %w", ErrInvalidProtocol, err)
	}
	return nil
}

func decodeEffect(data json.RawMessage) (effectEnvelope, error) {
	envelope, err := jsonwire.Decode[effectEnvelope](data)
	if err != nil {
		return effectEnvelope{}, fmt.Errorf("%w: decode effect: %w", ErrInvalidProtocol, err)
	}
	if err := envelope.validate(); err != nil {
		return effectEnvelope{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	return envelope, nil
}

// failedSettlement settles a dispatcher Effect as definitely failed with
// cause as its diagnostic payload.
func failedSettlement(cause error) (run.Settlement, error) {
	return (dispatchFailure{Diagnostic: run.NormalizeDiagnostic(cause.Error())}).settlement(run.SettlementStatusFailed)
}

func decodeSettlement(settlement run.Settlement) (resultEnvelope, string, error) {
	if settlement.Status() != run.SettlementStatusSucceeded {
		failure, err := jsonwire.Decode[dispatchFailure](settlement.Payload().JSON())
		if err != nil {
			return resultEnvelope{}, "", err
		}
		diagnostic, err := failure.diagnostic(settlement.Status())
		return resultEnvelope{}, diagnostic, err
	}
	envelope, err := decodeResult(settlement.Payload().JSON())
	return envelope, "", err
}

func decodeResult(data json.RawMessage) (resultEnvelope, error) {
	envelope, err := jsonwire.Decode[resultEnvelope](data)
	if err != nil {
		return resultEnvelope{}, fmt.Errorf("%w: decode result: %w", ErrInvalidProtocol, err)
	}
	if err := envelope.validate(); err != nil {
		return resultEnvelope{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	return envelope, nil
}
