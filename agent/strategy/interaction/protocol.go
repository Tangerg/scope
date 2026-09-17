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
	"github.com/Tangerg/scope/core/chat"
)

type operation string

const (
	operationResultCommit  operation = "result_commit"
	operationModelCall     operation = "model_call"
	operationToolCall      operation = "tool_call"
	operationWaitOpened    operation = "wait_opened"
	operationInputResponse operation = "input_response"
	operationSteer         operation = "steer"
)

type effectEnvelope struct {
	ResultCommit *resultCommit        `json:"result_commit,omitempty"`
	Operation    operation            `json:"operation"`
	ModelCall    *modelCall           `json:"model_call,omitempty"`
	ToolCall     *toolDispatchRequest `json:"tool_call,omitempty"`
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
	Resume     *toolResume `json:"resume,omitempty"`
}

type toolResume struct {
	Checkpoint    toolCheckpoint  `json:"checkpoint"`
	InputResponse json.RawMessage `json:"input_response"`
}

type signalEnvelope struct {
	Receipt       *ResultReceipt      `json:"receipt,omitempty"`
	Operation     operation           `json:"operation"`
	ModelResult   *modelCallResult    `json:"model_result,omitempty"`
	ToolResult    *toolDispatchResult `json:"tool_result,omitempty"`
	WaitOpened    *toolInputRequest   `json:"wait_opened,omitempty"`
	InputResponse json.RawMessage     `json:"input_response,omitempty"`
	Steer         *steerInput         `json:"steer,omitempty"`
}

type modelCallResult struct {
	Response            *chat.Response `json:"response,omitempty"`
	ReplacementMessages []chat.Message `json:"replacement_messages,omitempty"`
	Error               string         `json:"error,omitempty"`
	HostError           string         `json:"host_error,omitempty"`
}

type steerInput struct {
	Messages []chat.Message `json:"messages"`
}

type toolCallResult struct {
	Rejected            bool            `json:"rejected,omitempty"`
	Result              chat.ToolResult `json:"result"`
	Direct              bool            `json:"direct"`
	AdvertisedToolNames []string        `json:"advertised_tool_names,omitempty"`
}

type toolDispatchResult struct {
	Completion *toolCallResult `json:"completion,omitempty"`
	Checkpoint *toolCheckpoint `json:"checkpoint,omitempty"`
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
	if e.Operation == operationResultCommit {
		if e.ResultCommit == nil || e.ModelCall != nil || e.ToolCall != nil {
			return fmt.Errorf("%w: invalid result commit effect", ErrInvalidProtocol)
		}
		return e.ResultCommit.validate()
	}
	if e.ResultCommit != nil {
		return fmt.Errorf("%w: unexpected result commit", ErrInvalidProtocol)
	}

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
	if resume.Checkpoint.PauseCount == ^uint64(0) {
		return fmt.Errorf("%w: Tool input pause count is exhausted", ErrInvalidProtocol)
	}
	_, err := resume.Checkpoint.InputRequest.validateResponse(resume.InputResponse)
	return err
}

func (s signalEnvelope) validate() error {
	if s.Operation == operationResultCommit {
		if s.Receipt == nil || s.ModelResult != nil || s.ToolResult != nil || s.WaitOpened != nil || len(s.InputResponse) != 0 || s.Steer != nil {
			return fmt.Errorf("%w: invalid result receipt", ErrInvalidProtocol)
		}
		return s.Receipt.Validate()
	}
	if s.Receipt != nil {
		return fmt.Errorf("%w: unexpected result receipt", ErrInvalidProtocol)
	}

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
	result := s.ModelResult
	modes := 0
	if result.Response != nil {
		modes++
	}
	if result.Error != "" {
		modes++
	}
	if result.HostError != "" {
		modes++
	}
	if modes != 1 {
		return fmt.Errorf("%w: model_result requires exactly one response, provider error, or host error", ErrInvalidProtocol)
	}
	if result.Response != nil {
		if err := result.Response.Validate(); err != nil {
			return fmt.Errorf("%w: model_result response: %w", ErrInvalidProtocol, err)
		}
		if result.ReplacementMessages != nil && len(result.ReplacementMessages) == 0 {
			return fmt.Errorf("%w: replacement messages must not be empty", ErrInvalidProtocol)
		}
		for index := range result.ReplacementMessages {
			if err := result.ReplacementMessages[index].Validate(); err != nil {
				return fmt.Errorf("%w: model_result replacement message %d: %w", ErrInvalidProtocol, index, err)
			}
		}
	} else if result.ReplacementMessages != nil {
		return fmt.Errorf("%w: failed model_result cannot carry replacement messages", ErrInvalidProtocol)
	}
	return nil
}

func (s signalEnvelope) validateToolResult() error {
	if s.ModelResult != nil || s.ToolResult == nil || s.WaitOpened != nil || len(s.InputResponse) != 0 || s.Steer != nil {
		return fmt.Errorf("%w: tool_result signal has an invalid payload set", ErrInvalidProtocol)
	}
	return s.ToolResult.validate()
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
	t.Result = t.Result.Clone()
	t.AdvertisedToolNames = slices.Clone(t.AdvertisedToolNames)
	return t
}

func (t toolCallResult) validate() error {
	if t.Rejected && !t.Result.IsError {
		return fmt.Errorf("%w: rejected result must be an error", ErrInvalidProtocol)
	}
	if err := t.Result.Validate(); err != nil {
		return fmt.Errorf("%w: tool_result: %w", ErrInvalidProtocol, err)
	}
	if t.Direct && t.Result.IsError {
		return fmt.Errorf("%w: failed tool_result cannot be direct", ErrInvalidProtocol)
	}
	if t.Result.IsError && len(t.AdvertisedToolNames) != 0 {
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
	if t.Result.ID != call.ID || t.Result.Name != call.Name {
		return fmt.Errorf("%w: Tool result does not match its call", ErrInvalidProtocol)
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
	var envelope effectEnvelope
	if err := jsonv2.Unmarshal(data, &envelope, jsonv2.RejectUnknownMembers(true)); err != nil {
		return effectEnvelope{}, fmt.Errorf("%w: decode effect: %w", ErrInvalidProtocol, err)
	}
	if err := envelope.validate(); err != nil {
		return effectEnvelope{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	return envelope, nil
}

func decodeSignal(data json.RawMessage) (signalEnvelope, error) {
	var envelope signalEnvelope
	if err := jsonv2.Unmarshal(data, &envelope, jsonv2.RejectUnknownMembers(true)); err != nil {
		return signalEnvelope{}, fmt.Errorf("%w: decode signal: %w", ErrInvalidProtocol, err)
	}
	if err := envelope.validate(); err != nil {
		return signalEnvelope{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
	}
	return envelope, nil
}
