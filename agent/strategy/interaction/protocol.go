package interaction

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
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
	operationWaitOpened    operation = "wait_opened"
	operationInputResponse operation = "input_response"
	operationSteer         operation = "steer"
)

type effectEnvelope struct {
	Operation operation            `json:"operation"`
	ModelCall *modelCall           `json:"model_call,omitzero"`
	ToolCall  *toolDispatchRequest `json:"tool_call,omitzero"`
}

type modelCall struct {
	ModelCallSequence     uint64           `json:"model_call_sequence"`
	Request               chat.Request     `json:"request"`
	AdvertisedToolNames   []string         `json:"advertised_tool_names,omitempty"`
	AppliedSteerSignalIDs []agent.SignalID `json:"applied_steer_signal_ids,omitempty"`
}

type toolCall struct {
	ModelCallSequence uint64        `json:"model_call_sequence"`
	ToolCallIndex     uint32        `json:"tool_call_index"`
	Call              chat.ToolCall `json:"call"`
}

func (t *toolCall) UnmarshalJSON(data []byte) error {
	type wire toolCall
	decoded, err := jsonwire.Decode[wire](data, "tool_call_index")
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

type toolResume struct {
	Checkpoint    toolCheckpoint  `json:"checkpoint"`
	InputResponse json.RawMessage `json:"input_response"`
}

type signalEnvelope struct {
	Operation     operation           `json:"operation"`
	ModelResult   *modelCallResult    `json:"model_result,omitzero"`
	ToolResult    *toolDispatchResult `json:"tool_result,omitzero"`
	WaitOpened    *toolInputRequest   `json:"wait_opened,omitzero"`
	InputResponse json.RawMessage     `json:"input_response,omitzero"`
	Steer         *steerInput         `json:"steer,omitzero"`
}

type modelCallResult struct {
	Response            *chat.Response `json:"response,omitzero"`
	ReplacementMessages []chat.Message `json:"replacement_messages,omitempty"`
	HostError           string         `json:"host_error,omitempty"`
}

func (m modelCallResult) validate() error {
	modes := 0
	for _, present := range []bool{m.Response != nil, m.HostError != ""} {
		if present {
			modes++
		}
	}
	if modes != 1 {
		return fmt.Errorf("%w: model_result requires exactly one response or host error", ErrInvalidProtocol)
	}
	if m.Response == nil {
		if m.ReplacementMessages != nil {
			return fmt.Errorf("%w: failed model_result cannot carry replacement messages", ErrInvalidProtocol)
		}
		return nil
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

func (m modelCallResult) settlement(id agent.EffectID, maxBytes int) (agent.Settlement, error) {
	signal := signalEnvelope{Operation: operationModelCall, ModelResult: &m}
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
	return agent.NewSettlement(id, agent.SettlementStatusSucceeded, payload.JSON())
}

type steerInput struct {
	Messages []chat.Message `json:"messages" jsonschema:"minItems=1"`
}

// toolCallResult is what executing one ToolCall adds: its output, error, and
// admission facts. The call owns the ID and name that the model-facing
// chat.ToolResult carries, so they are never stored here.
type toolCallResult struct {
	Rejected            bool            `json:"rejected,omitzero"`
	Output              chat.ToolOutput `json:"output"`
	IsError             bool            `json:"is_error,omitzero"`
	Direct              bool            `json:"direct"`
	AdvertisedToolNames []string        `json:"advertised_tool_names,omitempty"`
}

// newToolCallResult keeps what a model-facing result adds to the call it answers.
func newToolCallResult(result chat.ToolResult) toolCallResult {
	return toolCallResult{Output: result.Output.Clone(), IsError: result.IsError}
}

// toolResult renders the result the model sees for call.
func (t toolCallResult) toolResult(call chat.ToolCall) chat.ToolResult {
	return chat.ToolResult{ID: call.ID, Name: call.Name, Output: t.Output.Clone(), IsError: t.IsError}
}

type toolDispatchResult struct {
	Completion *toolCallResult `json:"completion,omitzero"`
	Checkpoint *toolCheckpoint `json:"checkpoint,omitzero"`
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
		Operation: operationModelCall,
		ModelCall: &modelCall{
			ModelCallSequence:     modelCallSequence,
			Request:               *cloned,
			AdvertisedToolNames:   slices.Clone(advertisedToolNames),
			AppliedSteerSignalIDs: slices.Clone(appliedSteerSignalIDs),
		},
	}, nil
}

func newToolEffect(call toolDispatchRequest) (effectEnvelope, error) {
	envelope := effectEnvelope{Operation: operationToolCall, ToolCall: &call}
	if err := envelope.validateToolCall(); err != nil {
		return effectEnvelope{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	return envelope, nil
}

func (e effectEnvelope) validate() error {
	switch e.Operation {
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
	if err := resume.Checkpoint.validate(); err != nil {
		return err
	}
	if resume.Checkpoint.PauseCount == math.MaxUint64 {
		return fmt.Errorf("%w: Tool input pause count is exhausted", ErrInvalidProtocol)
	}
	_, err := resume.Checkpoint.InputRequest.validateResponse(resume.InputResponse)
	return err
}

func (s signalEnvelope) validate() error {
	switch s.Operation {
	case operationModelCall:
		return s.validateModelResult()
	case operationToolCall:
		return s.validateToolResult()
	case operationWaitOpened:
		return s.validateWaitOpened()
	case operationInputResponse:
		return s.validateInputResponse()
	case operationSteer:
		return s.validateSteer()
	default:
		return fmt.Errorf("%w: unsupported signal protocol", ErrInvalidProtocol)
	}
}

func (s signalEnvelope) validateModelResult() error {
	if s.ModelResult == nil || s.ToolResult != nil || s.WaitOpened != nil || len(s.InputResponse) != 0 || s.Steer != nil {
		return fmt.Errorf("%w: model_result signal has an invalid payload set", ErrInvalidProtocol)
	}
	return s.ModelResult.validate()
}

func (s signalEnvelope) validateToolResult() error {
	if s.ModelResult != nil || s.ToolResult == nil || s.WaitOpened != nil || len(s.InputResponse) != 0 || s.Steer != nil {
		return fmt.Errorf("%w: tool_result signal has an invalid payload set", ErrInvalidProtocol)
	}
	return s.ToolResult.validate()
}

func (t toolDispatchResult) settlement(id agent.EffectID) (agent.Settlement, error) {
	if err := t.validate(); err != nil {
		return agent.Settlement{}, err
	}
	payload, err := agent.EncodePayload(signalEnvelope{Operation: operationToolCall, ToolResult: &t})
	if err != nil {
		return agent.Settlement{}, err
	}
	return agent.NewSettlement(id, agent.SettlementStatusSucceeded, payload.JSON())
}

func (t toolDispatchResult) validate() error {
	if (t.Completion == nil) == (t.Checkpoint == nil) {
		return fmt.Errorf("%w: Tool dispatch requires one result or checkpoint", ErrInvalidProtocol)
	}
	if t.Checkpoint != nil {
		return t.Checkpoint.validate()
	}
	return t.Completion.validate()
}

func (t toolCallResult) clone() toolCallResult {
	t.Output = t.Output.Clone()
	t.AdvertisedToolNames = slices.Clone(t.AdvertisedToolNames)
	return t
}

func (t toolCallResult) disposition() ResultDisposition {
	switch {
	case t.Rejected:
		return ResultRejected
	case t.IsError:
		return ResultFailed
	default:
		return ResultSucceeded
	}
}

func (t toolCallResult) validate() error {
	if t.Rejected && !t.IsError {
		return fmt.Errorf("%w: rejected result must be an error", ErrInvalidProtocol)
	}
	if err := t.Output.Validate(); err != nil {
		return fmt.Errorf("%w: tool_result: %w", ErrInvalidProtocol, err)
	}
	if t.Direct && t.IsError {
		return fmt.Errorf("%w: failed tool_result cannot be direct", ErrInvalidProtocol)
	}
	if t.IsError && len(t.AdvertisedToolNames) != 0 {
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

func (s signalEnvelope) validateWaitOpened() error {
	if s.ModelResult != nil || s.ToolResult != nil || s.WaitOpened == nil || len(s.InputResponse) != 0 || s.Steer != nil {
		return fmt.Errorf("%w: wait_opened signal has an invalid payload set", ErrInvalidProtocol)
	}
	if !s.WaitOpened.valid() {
		return fmt.Errorf("%w: %w", ErrInvalidProtocol, ErrInvalidToolInputRequest)
	}
	return nil
}

func (s signalEnvelope) validateInputResponse() error {
	if s.ModelResult != nil || s.ToolResult != nil || s.WaitOpened != nil || len(s.InputResponse) == 0 || s.Steer != nil {
		return fmt.Errorf("%w: input_response signal has an invalid payload set", ErrInvalidProtocol)
	}
	if _, err := parseToolInputJSON(s.InputResponse); err != nil {
		return fmt.Errorf("%w: input_response: %w", ErrInvalidProtocol, err)
	}
	return nil
}

func (s signalEnvelope) validateSteer() error {
	if s.ModelResult != nil || s.ToolResult != nil || s.WaitOpened != nil || len(s.InputResponse) != 0 || s.Steer == nil {
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
