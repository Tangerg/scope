package interaction

import (
	"bytes"
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
	operationToolBatch     operation = "tool_batch"
	operationWaitOpened    operation = "wait_opened"
	operationInputResponse operation = "input_response"
	operationSteer         operation = "steer"
)

func (o operation) valid() bool {
	return o == operationModelCall || o == operationToolBatch ||
		o == operationWaitOpened || o == operationInputResponse || o == operationSteer
}

type effectEnvelope struct {
	Operation operation      `json:"operation"`
	ModelCall *modelCall     `json:"model_call,omitempty"`
	ToolBatch *toolBatchCall `json:"tool_batch,omitempty"`
}

type modelCall struct {
	ModelCallSequence     uint32           `json:"model_call_sequence"`
	Request               chat.Request     `json:"request"`
	AdvertisedToolNames   []string         `json:"advertised_tool_names,omitempty"`
	AppliedSteerSignalIDs []agent.SignalID `json:"applied_steer_signal_ids,omitempty"`
}

type toolBatchCall struct {
	ModelCallSequence  uint32          `json:"model_call_sequence"`
	FirstToolCallIndex uint32          `json:"first_tool_call_index"`
	Calls              []chat.ToolCall `json:"calls"`
	Checkpoint         *toolCheckpoint `json:"checkpoint,omitempty"`
	InputResponse      json.RawMessage `json:"input_response,omitempty"`
}

type signalEnvelope struct {
	Operation     operation         `json:"operation"`
	ModelResult   *modelCallResult  `json:"model_result,omitempty"`
	ToolResult    *toolBatchResult  `json:"tool_result,omitempty"`
	WaitOpened    *inputRequestWire `json:"wait_opened,omitempty"`
	InputResponse json.RawMessage   `json:"input_response,omitempty"`
	Steer         *steerInput       `json:"steer,omitempty"`
}

type modelCallResult struct {
	Response          *chat.Response `json:"response,omitempty"`
	EffectiveMessages []chat.Message `json:"effective_messages,omitempty"`
	Error             string         `json:"error,omitempty"`
	HostError         string         `json:"host_error,omitempty"`
}

type steerInput struct {
	Messages []chat.Message `json:"messages"`
}

type toolBatchResult struct {
	Results             []chat.ToolResult `json:"results"`
	Direct              bool              `json:"direct"`
	AdvertisedToolNames []string          `json:"advertised_tool_names,omitempty"`
	Checkpoint          *toolCheckpoint   `json:"checkpoint,omitempty"`
	HostError           string            `json:"host_error,omitempty"`
}

type toolCheckpoint struct {
	CompletedResults    []chat.ToolResult `json:"completed_results"`
	AdvertisedToolNames []string          `json:"advertised_tool_names,omitempty"`
	NextToolCallIndex   uint32            `json:"next_tool_call_index"`
	PauseCount          uint32            `json:"pause_count"`
	InputRequest        inputRequestWire  `json:"input_request"`
}

type inputRequestWire struct {
	Prompt            json.RawMessage `json:"prompt"`
	ResponseSchema    json.RawMessage `json:"response_schema"`
	ContinuationState json.RawMessage `json:"continuation_state"`
}

func wireInputRequest(request ToolInputRequest) inputRequestWire {
	return inputRequestWire{
		Prompt: request.Prompt(), ResponseSchema: request.ResponseSchema(),
		ContinuationState: request.ContinuationState(),
	}
}

func (i inputRequestWire) inputRequest() (ToolInputRequest, error) {
	return NewToolInputRequest(i.Prompt, i.ResponseSchema, i.ContinuationState)
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

func newToolBatchEffect(
	modelCallSequence uint32,
	firstToolCallIndex uint32,
	calls []chat.ToolCall,
	checkpoint *toolCheckpoint,
	inputResponse json.RawMessage,
) (effectEnvelope, error) {
	if modelCallSequence == 0 {
		return effectEnvelope{}, errors.New("interaction: model call sequence is required")
	}
	if len(calls) == 0 {
		return effectEnvelope{}, errors.New("interaction: Tool batch is empty")
	}
	if uint64(firstToolCallIndex)+uint64(len(calls)) > uint64(^uint32(0))+1 {
		return effectEnvelope{}, errors.New("interaction: Tool batch index range overflows")
	}
	cloned := slices.Clone(calls)
	for index := range cloned {
		if err := cloned[index].Validate(); err != nil {
			return effectEnvelope{}, fmt.Errorf("interaction: tool batch call %d: %w", index, err)
		}
	}
	batch := &toolBatchCall{
		ModelCallSequence:  modelCallSequence,
		FirstToolCallIndex: firstToolCallIndex,
		Calls:              cloned,
	}
	if checkpoint != nil {
		if err := checkpoint.validate(cloned); err != nil {
			return effectEnvelope{}, err
		}
		response, err := checkpoint.InputRequest.inputRequest()
		if err != nil {
			return effectEnvelope{}, err
		}
		inputResponse, err = response.validateResponse(inputResponse)
		if err != nil {
			return effectEnvelope{}, err
		}
		clonedCheckpoint := checkpoint.clone()
		batch.Checkpoint = &clonedCheckpoint
		batch.InputResponse = inputResponse
	} else if len(inputResponse) != 0 {
		return effectEnvelope{}, errors.New("interaction: input response requires a tool checkpoint")
	}
	return effectEnvelope{
		Operation: operationToolBatch,
		ToolBatch: batch,
	}, nil
}

func (e effectEnvelope) validate() error {
	if e.Operation != operationModelCall && e.Operation != operationToolBatch {
		return errors.New("interaction: unsupported effect protocol")
	}
	switch e.Operation {
	case operationModelCall:
		return e.validateModelCall()
	case operationToolBatch:
		return e.validateToolBatch()
	}
	return nil
}

func (e effectEnvelope) validateModelCall() error {
	if e.ModelCall == nil || e.ToolBatch != nil || e.ModelCall.ModelCallSequence == 0 {
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

func (e effectEnvelope) validateToolBatch() error {
	if e.ModelCall != nil || e.ToolBatch == nil ||
		e.ToolBatch.ModelCallSequence == 0 || len(e.ToolBatch.Calls) == 0 ||
		uint64(e.ToolBatch.FirstToolCallIndex)+uint64(len(e.ToolBatch.Calls)) > uint64(^uint32(0))+1 {
		return errors.New("interaction: tool_batch effect has an invalid payload set")
	}
	for index := range e.ToolBatch.Calls {
		if err := e.ToolBatch.Calls[index].Validate(); err != nil {
			return fmt.Errorf("interaction: tool_batch call %d: %w", index, err)
		}
	}
	if e.ToolBatch.Checkpoint == nil {
		if len(e.ToolBatch.InputResponse) != 0 {
			return errors.New("interaction: tool_batch input response requires a checkpoint")
		}
		return nil
	}
	if err := e.ToolBatch.Checkpoint.validate(e.ToolBatch.Calls); err != nil {
		return err
	}
	request, err := e.ToolBatch.Checkpoint.InputRequest.inputRequest()
	if err != nil {
		return err
	}
	_, err = request.validateResponse(e.ToolBatch.InputResponse)
	return err
}

func (s signalEnvelope) validate() error {
	if !s.Operation.valid() {
		return errors.New("interaction: unsupported signal protocol")
	}
	switch s.Operation {
	case operationModelCall:
		return s.validateModelResult()
	case operationToolBatch:
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
		if len(result.EffectiveMessages) == 0 {
			return errors.New("interaction: successful model_result requires effective messages")
		}
		for index := range result.EffectiveMessages {
			if err := result.EffectiveMessages[index].Validate(); err != nil {
				return fmt.Errorf("interaction: model_result effective message %d: %w", index, err)
			}
		}
	} else if len(result.EffectiveMessages) != 0 {
		return errors.New("interaction: failed model_result cannot carry effective messages")
	}
	return nil
}

func (s signalEnvelope) validateToolResult() error {
	if s.ModelResult != nil || s.ToolResult == nil || s.WaitOpened != nil || len(s.InputResponse) != 0 || s.Steer != nil {
		return errors.New("interaction: tool_result signal has an invalid payload set")
	}
	result := s.ToolResult
	modes := 0
	if len(result.Results) != 0 {
		modes++
	}
	if result.Checkpoint != nil {
		modes++
	}
	if result.HostError != "" {
		modes++
	}
	if modes != 1 {
		return errors.New("interaction: tool_result requires complete results, a checkpoint, or a host error")
	}
	switch {
	case result.HostError != "":
		if result.Direct || len(result.AdvertisedToolNames) != 0 {
			return errors.New("interaction: failed tool_result must carry only its host error")
		}
	case result.Checkpoint != nil:
		if result.Direct || len(result.AdvertisedToolNames) != 0 {
			return errors.New("interaction: paused tool_result must carry only its checkpoint")
		}
		if _, err := result.Checkpoint.InputRequest.inputRequest(); err != nil {
			return err
		}
	default:
		for index := range result.Results {
			if err := result.Results[index].Validate(); err != nil {
				return fmt.Errorf("interaction: tool_result %d: %w", index, err)
			}
			if result.Direct && result.Results[index].IsError {
				return fmt.Errorf("interaction: direct tool_result %d failed", index)
			}
		}
	}
	if err := validateAdvertisedToolNames(result.AdvertisedToolNames); err != nil {
		return fmt.Errorf("interaction: tool_result advertised Tools: %w", err)
	}
	return nil
}

func (s signalEnvelope) validateWaitOpened() error {
	if s.ModelResult != nil || s.ToolResult != nil || s.WaitOpened == nil || len(s.InputResponse) != 0 || s.Steer != nil {
		return errors.New("interaction: wait_opened signal has an invalid payload set")
	}
	_, err := s.WaitOpened.inputRequest()
	return err
}

func (s signalEnvelope) validateInputResponse() error {
	if s.ModelResult != nil || s.ToolResult != nil || s.WaitOpened != nil || len(s.InputResponse) == 0 || s.Steer != nil {
		return errors.New("interaction: input_response signal has an invalid payload set")
	}
	if _, err := canonicalJSON(s.InputResponse); err != nil {
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

func (t toolCheckpoint) clone() toolCheckpoint {
	cloned := t
	cloned.CompletedResults = cloneToolResults(t.CompletedResults)
	cloned.AdvertisedToolNames = slices.Clone(t.AdvertisedToolNames)
	cloned.InputRequest.Prompt = bytes.Clone(t.InputRequest.Prompt)
	cloned.InputRequest.ResponseSchema = bytes.Clone(t.InputRequest.ResponseSchema)
	cloned.InputRequest.ContinuationState = bytes.Clone(t.InputRequest.ContinuationState)
	return cloned
}

func (t toolCheckpoint) validate(calls []chat.ToolCall) error {
	if t.PauseCount == 0 || t.NextToolCallIndex != uint32(len(t.CompletedResults)) ||
		int(t.NextToolCallIndex) >= len(calls) {
		return errors.New("interaction: invalid tool checkpoint position")
	}
	if _, err := t.InputRequest.inputRequest(); err != nil {
		return fmt.Errorf("interaction: tool checkpoint input: %w", err)
	}
	if err := validateAdvertisedToolNames(t.AdvertisedToolNames); err != nil {
		return fmt.Errorf("interaction: tool checkpoint advertised Tools: %w", err)
	}
	for index := range t.CompletedResults {
		result := t.CompletedResults[index]
		if err := result.Validate(); err != nil {
			return fmt.Errorf("interaction: tool checkpoint result %d: %w", index, err)
		}
		if result.ID != calls[index].ID || result.Name != calls[index].Name {
			return fmt.Errorf("interaction: tool checkpoint result %d does not match call %q", index, calls[index].ID)
		}
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
