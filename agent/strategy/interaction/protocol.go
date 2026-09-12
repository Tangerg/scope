package interaction

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"slices"

	agent "github.com/Tangerg/scope/agent"
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

func (o operation) valid() bool {
	return o == operationModelCall || o == operationToolCall ||
		o == operationWaitOpened || o == operationInputResponse || o == operationSteer
}

type effectEnvelope struct {
	Operation operation            `json:"operation"`
	ModelCall *modelCall           `json:"model_call,omitempty"`
	ToolCall  *toolDispatchRequest `json:"tool_call,omitempty"`
}

type modelCall struct {
	ModelCallSequence     uint32           `json:"model_call_sequence"`
	Request               chat.Request     `json:"request"`
	AdvertisedToolNames   []string         `json:"advertised_tool_names,omitempty"`
	AppliedSteerSignalIDs []agent.SignalID `json:"applied_steer_signal_ids,omitempty"`
}

type toolCall struct {
	ModelCallSequence uint32        `json:"model_call_sequence"`
	ToolCallIndex     uint32        `json:"tool_call_index"`
	Call              chat.ToolCall `json:"call"`
}

func (t toolCall) validate() error {
	if t.ModelCallSequence == 0 {
		return errors.New("interaction: Tool call sequence is required")
	}
	if err := t.Call.Validate(); err != nil {
		return fmt.Errorf("interaction: tool_call: %w", err)
	}
	return nil
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
	Operation     operation           `json:"operation"`
	ModelResult   *modelCallResult    `json:"model_result,omitempty"`
	ToolResult    *toolDispatchResult `json:"tool_result,omitempty"`
	WaitOpened    *ToolInputRequest   `json:"wait_opened,omitempty"`
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
	Result              chat.ToolResult `json:"result"`
	Direct              bool            `json:"direct"`
	AdvertisedToolNames []string        `json:"advertised_tool_names,omitempty"`
}

type toolDispatchResult struct {
	Completion *toolCallResult `json:"completion,omitempty"`
	Checkpoint *toolCheckpoint `json:"checkpoint,omitempty"`
}

type toolCheckpoint struct {
	PauseCount   uint32           `json:"pause_count"`
	InputRequest ToolInputRequest `json:"input_request"`
}

func newModelEffect(
	request *chat.Request,
	modelCallSequence uint32,
	advertisedToolNames []string,
	appliedSteerSignalIDs []agent.SignalID,
) (effectEnvelope, error) {
	if request == nil {
		return effectEnvelope{}, errors.New("interaction: model request is nil")
	}
	if modelCallSequence == 0 {
		return effectEnvelope{}, errors.New("interaction: model call sequence is required")
	}
	if err := validateAdvertisedToolNames(advertisedToolNames); err != nil {
		return effectEnvelope{}, fmt.Errorf("interaction: advertised Tools: %w", err)
	}
	if len(appliedSteerSignalIDs) > 0 {
		if err := validateSteerSignalIDs(appliedSteerSignalIDs); err != nil {
			return effectEnvelope{}, fmt.Errorf("interaction: applied steer SignalIDs: %w", err)
		}
	}
	cloned := request.Clone()
	if err := cloned.Validate(); err != nil {
		return effectEnvelope{}, fmt.Errorf("interaction: model request: %w", err)
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
		return effectEnvelope{}, err
	}
	return envelope, nil
}

func (e effectEnvelope) validate() error {
	if e.Operation != operationModelCall && e.Operation != operationToolCall {
		return errors.New("interaction: unsupported effect protocol")
	}
	switch e.Operation {
	case operationModelCall:
		return e.validateModelCall()
	case operationToolCall:
		return e.validateToolCall()
	}
	return nil
}

func (e effectEnvelope) validateModelCall() error {
	if e.ModelCall == nil || e.ToolCall != nil || e.ModelCall.ModelCallSequence == 0 {
		return errors.New("interaction: model_call effect has an invalid payload set")
	}
	if err := e.ModelCall.Request.Validate(); err != nil {
		return fmt.Errorf("interaction: model_call request: %w", err)
	}
	if err := validateAdvertisedToolNames(e.ModelCall.AdvertisedToolNames); err != nil {
		return fmt.Errorf("interaction: model_call advertised Tools: %w", err)
	}
	if len(e.ModelCall.AppliedSteerSignalIDs) > 0 {
		if err := validateSteerSignalIDs(e.ModelCall.AppliedSteerSignalIDs); err != nil {
			return fmt.Errorf("interaction: model_call applied steer SignalIDs: %w", err)
		}
	}
	return nil
}

func (e effectEnvelope) validateToolCall() error {
	if e.ModelCall != nil || e.ToolCall == nil {
		return errors.New("interaction: tool_call effect has an invalid payload set")
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
	if resume.Checkpoint.PauseCount == ^uint32(0) {
		return errors.New("interaction: Tool input pause count is exhausted")
	}
	_, err := resume.Checkpoint.InputRequest.validateResponse(resume.InputResponse)
	return err
}

func (s signalEnvelope) validate() error {
	if !s.Operation.valid() {
		return errors.New("interaction: unsupported signal protocol")
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
	}
	return nil
}

func (s signalEnvelope) validateModelResult() error {
	if s.ModelResult == nil || s.ToolResult != nil || s.WaitOpened != nil || len(s.InputResponse) != 0 || s.Steer != nil {
		return errors.New("interaction: model_result signal has an invalid payload set")
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
		return errors.New("interaction: model_result requires exactly one response, provider error, or host error")
	}
	if result.Response != nil {
		if err := result.Response.Validate(); err != nil {
			return fmt.Errorf("interaction: model_result response: %w", err)
		}
		if result.ReplacementMessages != nil && len(result.ReplacementMessages) == 0 {
			return errors.New("interaction: replacement messages must not be empty")
		}
		for index := range result.ReplacementMessages {
			if err := result.ReplacementMessages[index].Validate(); err != nil {
				return fmt.Errorf("interaction: model_result replacement message %d: %w", index, err)
			}
		}
	} else if result.ReplacementMessages != nil {
		return errors.New("interaction: failed model_result cannot carry replacement messages")
	}
	return nil
}

func (s signalEnvelope) validateToolResult() error {
	if s.ModelResult != nil || s.ToolResult == nil || s.WaitOpened != nil || len(s.InputResponse) != 0 || s.Steer != nil {
		return errors.New("interaction: tool_result signal has an invalid payload set")
	}
	return s.ToolResult.validate()
}

func (t toolDispatchResult) validate() error {
	if (t.Completion == nil) == (t.Checkpoint == nil) {
		return errors.New("interaction: Tool dispatch requires one result or checkpoint")
	}
	if t.Checkpoint != nil {
		return t.Checkpoint.validate()
	}
	return t.Completion.validate()
}

func (t toolCallResult) validate() error {
	if err := t.Result.Validate(); err != nil {
		return fmt.Errorf("interaction: tool_result: %w", err)
	}
	if t.Direct && t.Result.IsError {
		return errors.New("interaction: failed tool_result cannot be direct")
	}
	if t.Result.IsError && len(t.AdvertisedToolNames) != 0 {
		return errors.New("interaction: failed tool_result cannot advertise Tools")
	}
	if err := validateAdvertisedToolNames(t.AdvertisedToolNames); err != nil {
		return fmt.Errorf("interaction: tool_result advertised Tools: %w", err)
	}
	return nil
}

func (t toolCallResult) validateCall(call chat.ToolCall) error {
	if err := t.validate(); err != nil {
		return err
	}
	if t.Result.ID != call.ID || t.Result.Name != call.Name {
		return errors.New("interaction: Tool result does not match its call")
	}
	return nil
}

func (s signalEnvelope) validateWaitOpened() error {
	if s.ModelResult != nil || s.ToolResult != nil || s.WaitOpened == nil || len(s.InputResponse) != 0 || s.Steer != nil {
		return errors.New("interaction: wait_opened signal has an invalid payload set")
	}
	if !s.WaitOpened.Valid() {
		return ErrInvalidToolInputRequest
	}
	return nil
}

func (s signalEnvelope) validateInputResponse() error {
	if s.ModelResult != nil || s.ToolResult != nil || s.WaitOpened != nil || len(s.InputResponse) == 0 || s.Steer != nil {
		return errors.New("interaction: input_response signal has an invalid payload set")
	}
	if _, err := parseToolInputJSON(s.InputResponse); err != nil {
		return fmt.Errorf("interaction: input_response: %w", err)
	}
	return nil
}

func (s signalEnvelope) validateSteer() error {
	if s.ModelResult != nil || s.ToolResult != nil || s.WaitOpened != nil || len(s.InputResponse) != 0 || s.Steer == nil {
		return errors.New("interaction: steer signal has an invalid payload set")
	}
	return validateSteeringMessages(s.Steer.Messages)
}

func (t toolCheckpoint) validate() error {
	if t.PauseCount == 0 {
		return errors.New("interaction: tool checkpoint pause count is required")
	}
	if !t.InputRequest.Valid() {
		return fmt.Errorf("interaction: tool checkpoint input: %w", ErrInvalidToolInputRequest)
	}
	return nil
}

func encodeProtocol(value any) (json.RawMessage, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("interaction: encode protocol payload: %w", err)
	}
	return payload, nil
}

func decodeEffect(data json.RawMessage) (effectEnvelope, error) {
	var envelope effectEnvelope
	if err := jsonv2.Unmarshal(data, &envelope, jsonv2.RejectUnknownMembers(true)); err != nil {
		return effectEnvelope{}, fmt.Errorf("interaction: decode effect: %w", err)
	}
	if err := envelope.validate(); err != nil {
		return effectEnvelope{}, err
	}
	return envelope, nil
}

func decodeSignal(data json.RawMessage) (signalEnvelope, error) {
	var envelope signalEnvelope
	if err := jsonv2.Unmarshal(data, &envelope, jsonv2.RejectUnknownMembers(true)); err != nil {
		return signalEnvelope{}, fmt.Errorf("interaction: decode signal: %w", err)
	}
	if err := envelope.validate(); err != nil {
		return signalEnvelope{}, err
	}
	return envelope, nil
}
