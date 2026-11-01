package agent

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"slices"

	"github.com/Tangerg/scope/core/tool"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/jsonschema"
)

type operation string

const (
	operationPrepare   operation = "prepare_context"
	operationModelCall operation = "model_call"
	operationToolCall  operation = "tool_call"
	operationSteer     operation = "steer"
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
	ModelCallSequence     uint64         `json:"model_call_sequence"`
	Request               Input          `json:"request"`
	AdvertisedToolNames   []string       `json:"advertised_tool_names,omitempty"`
	AppliedSteerSignalIDs []run.SignalID `json:"applied_steer_signal_ids,omitempty"`
}

// toolCall is a Tool child's input. Which model call requested it belongs to
// the child's ChildKey, never to a copy here that any parent could write.
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
	InputResponse json.RawMessage   `json:"input_response"`
}

// signalEnvelope carries exactly one result or input; the member present
// names its operation.
type signalEnvelope struct {
	Prepared    *preparedContext    `json:"prepared_context,omitzero"`
	ModelResult *modelCallResult    `json:"model_result,omitzero"`
	ToolResult  *toolDispatchResult `json:"tool_result,omitzero"`
	Steer       *steerInput         `json:"steer,omitzero"`
}

// modelCallResult is a successful model call's settlement payload. The
// settlement status owns whether the call succeeded; a failed call settles
// with its diagnostic instead.
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
	signal := signalEnvelope{ModelResult: &m}
	if err := signal.validateModelResult(); err != nil {
		return run.Settlement{}, err
	}
	payload, err := run.EncodePayload(signal)
	if err != nil {
		return run.Settlement{}, err
	}
	if len(payload.JSON()) > maxBytes {
		return run.Settlement{}, ErrModelResponseTooLarge
	}
	return run.NewSettlement(run.SettlementStatusSucceeded, payload.JSON())
}

type steerInput struct {
	Messages []chat.Message `json:"messages"`
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
	// both facts lets Step stop the child without inventing external uncertainty.
	AdvertisementFailure string `json:"advertisement_failure,omitzero"`
}

func newModelEffect(
	request *Input,
	modelCallSequence uint64,
	advertisedToolNames []string,
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
			AdvertisedToolNames:   slices.Clone(advertisedToolNames),
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
	if err := e.ModelCall.Request.Validate(); err != nil {
		return fmt.Errorf("%w: model_call request: %w", ErrInvalidProtocol, err)
	}
	if err := validateAdvertisedToolNames(e.ModelCall.AdvertisedToolNames); err != nil {
		return fmt.Errorf("%w: model_call advertised Tools: %w", ErrInvalidProtocol, err)
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
	if !resume.InputRequest.Valid() {
		return fmt.Errorf("%w: tool resume input: %w", ErrInvalidProtocol, tool.ErrInvalidInputRequest)
	}
	_, err := resume.InputRequest.Continuation(resume.InputResponse)
	return err
}

func (s signalEnvelope) operation() operation {
	switch {
	case s.Prepared != nil:
		return operationPrepare
	case s.ModelResult != nil:
		return operationModelCall
	case s.ToolResult != nil:
		return operationToolCall
	case s.Steer != nil:
		return operationSteer
	default:
		return ""
	}
}

func (s signalEnvelope) validate() error {
	switch s.operation() {
	case operationPrepare:
		if s.ModelResult != nil || s.ToolResult != nil || s.Steer != nil {
			return ErrInvalidProtocol
		}
		return s.Prepared.validate()
	case operationModelCall:
		return s.validateModelResult()
	case operationToolCall:
		return s.validateToolResult()
	case operationSteer:
		return s.validateSteer()
	default:
		return fmt.Errorf("%w: unsupported signal protocol", ErrInvalidProtocol)
	}
}

func (s signalEnvelope) validateModelResult() error {
	if s.ModelResult == nil || s.ToolResult != nil || s.Steer != nil {
		return fmt.Errorf("%w: model_result signal has an invalid payload set", ErrInvalidProtocol)
	}
	return s.ModelResult.validate()
}

func (s signalEnvelope) validateToolResult() error {
	if s.ModelResult != nil || s.ToolResult == nil || s.Steer != nil {
		return fmt.Errorf("%w: tool_result signal has an invalid payload set", ErrInvalidProtocol)
	}
	return s.ToolResult.validate()
}

func (t toolDispatchResult) settlement() (run.Settlement, error) {
	if err := t.validate(); err != nil {
		return run.Settlement{}, err
	}
	payload, err := run.EncodePayload(signalEnvelope{ToolResult: &t})
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

func (s signalEnvelope) validateSteer() error {
	if s.ModelResult != nil || s.ToolResult != nil || s.Steer == nil {
		return fmt.Errorf("%w: steer signal has an invalid payload set", ErrInvalidProtocol)
	}
	return validateSteeringMessages(s.Steer.Messages)
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

// failedSettlement settles a Dispatcher Effect as definitely failed with
// cause as its diagnostic payload.
func failedSettlement(cause error) (run.Settlement, error) {
	payload, err := jsonv2.Marshal(run.NormalizeDiagnostic(cause.Error()))
	if err != nil {
		return run.Settlement{}, err
	}
	return run.NewSettlement(run.SettlementStatusFailed, payload)
}

// decodeSettlement reads a Dispatcher settlement Signal. A failed settlement
// returns its diagnostic instead of a result envelope.
func decodeSettlement(signal run.Signal) (signalEnvelope, string, error) {
	settlement, err := run.ParseSettlement(signal)
	if err != nil {
		return signalEnvelope{}, "", fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	if settlement.Status() == run.SettlementStatusFailed {
		diagnostic, decodeErr := jsonwire.Decode[string](settlement.Payload())
		if decodeErr != nil || !run.ValidDiagnostic(diagnostic) {
			return signalEnvelope{}, "", fmt.Errorf("%w: failed settlement requires a diagnostic", ErrInvalidProtocol)
		}
		return signalEnvelope{}, diagnostic, nil
	}
	envelope, err := decodeSignal(settlement.Payload())
	return envelope, "", err
}

func decodeSignal(data json.RawMessage) (signalEnvelope, error) {
	envelope, err := jsonwire.Decode[signalEnvelope](data)
	if err != nil {
		return signalEnvelope{}, fmt.Errorf("%w: decode signal: %w", ErrInvalidProtocol, err)
	}
	if err := envelope.validate(); err != nil {
		return signalEnvelope{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	return envelope, nil
}

func (steerInput) JSONSchemaConstraints() jsonschema.Constraints {
	return jsonschema.Constraints{Properties: map[string]jsonschema.Constraints{
		"messages": {MinItems: new(uint64(minimumSteeringMessages))},
	}}
}
