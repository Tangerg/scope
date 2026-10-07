package interaction

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"slices"
	"strconv"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/internal/jsonwire"
	"github.com/Tangerg/scope/core/chat"
)

type operation string

const (
	operationModelCall     operation = "model_call"
	operationToolCall      operation = "tool_call"
	operationInputResponse operation = "input_response"
	operationSteer         operation = "steer"
)

// effectEnvelope carries exactly one request; the member present names its
// operation.
type effectEnvelope struct {
	ModelCall *modelCall           `json:"model_call,omitzero"`
	ToolCall  *toolDispatchRequest `json:"tool_call,omitzero"`
}

func (e effectEnvelope) operation() operation {
	switch {
	case e.ModelCall != nil:
		return operationModelCall
	case e.ToolCall != nil:
		return operationToolCall
	default:
		return ""
	}
}

type modelCall struct {
	ModelCallSequence     uint64           `json:"model_call_sequence"`
	Request               chat.Request     `json:"request"`
	AdvertisedToolNames   []string         `json:"advertised_tool_names,omitempty"`
	AppliedSteerSignalIDs []agent.SignalID `json:"applied_steer_signal_ids,omitempty"`
}

type toolCall struct {
	ModelCallSequence uint64        `json:"model_call_sequence"`
	ToolCallIndex     uint32        `json:"tool_call_index" jsonwire:"required"`
	Call              chat.ToolCall `json:"call"`
}

func (t *toolCall) UnmarshalJSON(data []byte) error {
	type wire toolCall
	decoded, err := jsonwire.Decode[wire](data)
	if err != nil {
		return err
	}
	*t = toolCall(decoded)
	return nil
}

func (t toolCall) validate() error {
	if t.ModelCallSequence == 0 {
		return fmt.Errorf("%w: Tool call sequence is required", ErrInvalidProtocol)
	}
	if err := t.Call.Validate(); err != nil {
		return fmt.Errorf("%w: tool_call: %w", ErrInvalidProtocol, err)
	}
	return nil
}

func (t toolCall) checkpointWaitKey(pauseCount uint64) (agent.WaitKey, error) {
	hash := sha256.New()
	hash.Write([]byte(strconv.FormatUint(uint64(t.ModelCallSequence), 10)))
	hash.Write([]byte{0})
	hash.Write([]byte(t.Call.ID))
	hash.Write([]byte{0})
	hash.Write([]byte(strconv.FormatUint(uint64(pauseCount), 10)))
	return agent.ParseWaitKey("interaction.input." + hex.EncodeToString(hash.Sum(nil)))
}

type toolDispatchRequest struct {
	Invocation toolCall    `json:"invocation"`
	Resume     *toolResume `json:"resume,omitzero"`
}

// toolResume continues the Tool with the input it requested. The Tool
// Execution owns how many times it has paused.
type toolResume struct {
	InputRequest  toolInputRequest `json:"input_request"`
	InputResponse json.RawMessage  `json:"input_response"`
}

// signalEnvelope carries exactly one result or input; the member present
// names its operation.
type signalEnvelope struct {
	ModelResult   *modelCallResult    `json:"model_result,omitzero"`
	ToolResult    *toolDispatchResult `json:"tool_result,omitzero"`
	InputResponse json.RawMessage     `json:"input_response,omitzero"`
	Steer         *steerInput         `json:"steer,omitzero"`
}

// modelCallResult is a successful model call's settlement payload. The
// settlement status owns whether the call succeeded; a failed call settles
// with its diagnostic instead.
type modelCallResult struct {
	Response            *chat.Response `json:"response"`
	ReplacementMessages []chat.Message `json:"replacement_messages,omitempty"`
}

func (m modelCallResult) validate() error {
	if m.Response == nil {
		return fmt.Errorf("%w: model_result requires a response", ErrInvalidProtocol)
	}
	if err := m.Response.Validate(); err != nil {
		return fmt.Errorf("%w: model_result response: %w", ErrInvalidProtocol, err)
	}
	if m.ReplacementMessages != nil && len(m.ReplacementMessages) == 0 {
		return fmt.Errorf("%w: replacement messages must not be empty", ErrInvalidProtocol)
	}
	for index := range m.ReplacementMessages {
		if err := m.ReplacementMessages[index].Validate(); err != nil {
			return fmt.Errorf("%w: model_result replacement message %d: %w", ErrInvalidProtocol, index, err)
		}
	}
	return nil
}

func (m modelCallResult) settlement(maxBytes int) (agent.Settlement, error) {
	signal := signalEnvelope{ModelResult: &m}
	if err := signal.validateModelResult(); err != nil {
		return agent.Settlement{}, err
	}
	payload, err := agent.EncodePayload(signal)
	if err != nil {
		return agent.Settlement{}, err
	}
	if len(payload.JSON()) > maxBytes {
		return agent.Settlement{}, ErrModelResponseTooLarge
	}
	return agent.NewSettlement(agent.SettlementStatusSucceeded, payload.JSON())
}

type steerInput struct {
	Messages []chat.Message `json:"messages" jsonschema:"minItems=1"`
}

// toolCallResult is what executing one ToolCall adds: its output, disposition,
// and advertisements. The call owns the ID and name that the model-facing
// chat.ToolResult carries, and the frozen ToolSet owns whether a successful
// result completes directly, so neither is stored here.
type toolCallResult struct {
	Disposition         ResultDisposition `json:"disposition"`
	Output              chat.ToolOutput   `json:"output"`
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
	Completion   *toolCallResult   `json:"completion,omitzero"`
	InputRequest *toolInputRequest `json:"input_request,omitzero"`
}

type toolCheckpoint struct {
	PauseCount   uint64           `json:"pause_count"`
	InputRequest toolInputRequest `json:"input_request"`
}

func newModelEffect(
	request *chat.Request,
	modelCallSequence uint64,
	advertisedToolNames []string,
	appliedSteerSignalIDs []agent.SignalID,
) (effectEnvelope, error) {
	if request == nil {
		return effectEnvelope{}, fmt.Errorf("%w: model request is nil", ErrInvalidProtocol)
	}
	if modelCallSequence == 0 {
		return effectEnvelope{}, fmt.Errorf("%w: model call sequence is required", ErrInvalidProtocol)
	}
	if err := validateAdvertisedToolNames(advertisedToolNames); err != nil {
		return effectEnvelope{}, fmt.Errorf("%w: advertised Tools: %w", ErrInvalidProtocol, err)
	}
	if len(appliedSteerSignalIDs) > 0 {
		if err := validateSteerSignalIDs(appliedSteerSignalIDs); err != nil {
			return effectEnvelope{}, fmt.Errorf("%w: applied steer SignalIDs: %w", ErrInvalidProtocol, err)
		}
	}
	cloned := request.Clone()
	if err := cloned.Validate(); err != nil {
		return effectEnvelope{}, fmt.Errorf("%w: model request: %w", ErrInvalidProtocol, err)
	}
	return effectEnvelope{
		ModelCall: &modelCall{
			ModelCallSequence:     modelCallSequence,
			Request:               *cloned,
			AdvertisedToolNames:   slices.Clone(advertisedToolNames),
			AppliedSteerSignalIDs: slices.Clone(appliedSteerSignalIDs),
		},
	}, nil
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
	if !resume.InputRequest.valid() {
		return fmt.Errorf("%w: tool resume input: %w", ErrInvalidProtocol, ErrInvalidToolInputRequest)
	}
	_, err := resume.InputRequest.validateResponse(resume.InputResponse)
	return err
}

func (s signalEnvelope) operation() operation {
	switch {
	case s.ModelResult != nil:
		return operationModelCall
	case s.ToolResult != nil:
		return operationToolCall
	case len(s.InputResponse) != 0:
		return operationInputResponse
	case s.Steer != nil:
		return operationSteer
	default:
		return ""
	}
}

func (s signalEnvelope) validate() error {
	switch s.operation() {
	case operationModelCall:
		return s.validateModelResult()
	case operationToolCall:
		return s.validateToolResult()
	case operationInputResponse:
		return s.validateInputResponse()
	case operationSteer:
		return s.validateSteer()
	default:
		return fmt.Errorf("%w: unsupported signal protocol", ErrInvalidProtocol)
	}
}

func (s signalEnvelope) validateModelResult() error {
	if s.ModelResult == nil || s.ToolResult != nil || len(s.InputResponse) != 0 || s.Steer != nil {
		return fmt.Errorf("%w: model_result signal has an invalid payload set", ErrInvalidProtocol)
	}
	return s.ModelResult.validate()
}

func (s signalEnvelope) validateToolResult() error {
	if s.ModelResult != nil || s.ToolResult == nil || len(s.InputResponse) != 0 || s.Steer != nil {
		return fmt.Errorf("%w: tool_result signal has an invalid payload set", ErrInvalidProtocol)
	}
	return s.ToolResult.validate()
}

func (t toolDispatchResult) settlement() (agent.Settlement, error) {
	if err := t.validate(); err != nil {
		return agent.Settlement{}, err
	}
	payload, err := agent.EncodePayload(signalEnvelope{ToolResult: &t})
	if err != nil {
		return agent.Settlement{}, err
	}
	return agent.NewSettlement(agent.SettlementStatusSucceeded, payload.JSON())
}

func (t toolDispatchResult) validate() error {
	if (t.Completion == nil) == (t.InputRequest == nil) {
		return fmt.Errorf("%w: Tool dispatch requires one result or input request", ErrInvalidProtocol)
	}
	if t.InputRequest != nil {
		if !t.InputRequest.valid() {
			return fmt.Errorf("%w: tool input: %w", ErrInvalidProtocol, ErrInvalidToolInputRequest)
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

func (s signalEnvelope) validateInputResponse() error {
	if s.ModelResult != nil || s.ToolResult != nil || len(s.InputResponse) == 0 || s.Steer != nil {
		return fmt.Errorf("%w: input_response signal has an invalid payload set", ErrInvalidProtocol)
	}
	if _, err := parseToolInputJSON(s.InputResponse); err != nil {
		return fmt.Errorf("%w: input_response: %w", ErrInvalidProtocol, err)
	}
	return nil
}

func (s signalEnvelope) validateSteer() error {
	if s.ModelResult != nil || s.ToolResult != nil || len(s.InputResponse) != 0 || s.Steer == nil {
		return fmt.Errorf("%w: steer signal has an invalid payload set", ErrInvalidProtocol)
	}
	return validateSteeringMessages(s.Steer.Messages)
}

func (t toolCheckpoint) validate() error {
	if t.PauseCount == 0 {
		return fmt.Errorf("%w: tool checkpoint pause count is required", ErrInvalidProtocol)
	}
	if !t.InputRequest.valid() {
		return fmt.Errorf("%w: tool checkpoint input: %w", ErrInvalidProtocol, ErrInvalidToolInputRequest)
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

// failedSettlement settles a Dispatcher Effect as definitely failed with
// cause as its diagnostic payload.
func failedSettlement(cause error) (agent.Settlement, error) {
	payload, err := jsonv2.Marshal(agent.NormalizeDiagnostic(cause.Error()))
	if err != nil {
		return agent.Settlement{}, err
	}
	return agent.NewSettlement(agent.SettlementStatusFailed, payload)
}

// decodeSettlement reads a Dispatcher settlement Signal. A failed settlement
// returns its diagnostic instead of a result envelope.
func decodeSettlement(signal agent.Signal) (signalEnvelope, string, error) {
	settlement, err := agent.ParseSettlement(signal)
	if err != nil {
		return signalEnvelope{}, "", fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	if settlement.Status() == agent.SettlementStatusFailed {
		diagnostic, decodeErr := jsonwire.Decode[string](settlement.Payload())
		if decodeErr != nil || !agent.ValidDiagnostic(diagnostic) {
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
