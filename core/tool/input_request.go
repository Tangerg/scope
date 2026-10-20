package tool

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/core/jsonschema"
)

var ErrInvalidInputRequest = errors.New("tool: invalid input request")

const MaxInputBytes = 1 << 20

// InputRequest freezes the prompt, answer contract, and Tool-owned checkpoint.
// The driving runtime binds it to one logical call and owns its wait identity.
type InputRequest struct {
	prompt            json.RawMessage
	responseSchema    jsonschema.Schema
	continuationState json.RawMessage
}

type InputRequestConfig struct {
	Prompt            json.RawMessage
	ResponseSchema    jsonschema.Schema
	ContinuationState json.RawMessage
}

// NewInputRequest validates and freezes a re-entry checkpoint. A Tool must
// request input before external effects, or retain enough state for safe re-entry.
func NewInputRequest(config InputRequestConfig) (InputRequest, error) {
	prompt, continuationState := config.Prompt, config.ContinuationState
	parsedPrompt, err := parseInputJSON(prompt)
	if err != nil {
		return InputRequest{}, fmt.Errorf("%w: prompt: %w", ErrInvalidInputRequest, err)
	}
	schema := config.ResponseSchema
	if !schema.Valid() {
		return InputRequest{}, fmt.Errorf("%w: response schema is invalid", ErrInvalidInputRequest)
	}
	continuation, err := parseInputJSON(continuationState)
	if err != nil {
		return InputRequest{}, fmt.Errorf("%w: continuation state: %w", ErrInvalidInputRequest, err)
	}
	return InputRequest{
		prompt: parsedPrompt, responseSchema: schema, continuationState: continuation,
	}, nil
}

func (i InputRequest) Valid() bool {
	return len(i.prompt) > 0 && i.responseSchema.Valid() && len(i.continuationState) > 0
}

type inputRequestWire struct {
	Prompt            json.RawMessage `json:"prompt"`
	ResponseSchema    json.RawMessage `json:"response_schema"`
	ContinuationState json.RawMessage `json:"continuation_state"`
}

func (i InputRequest) MarshalJSON() ([]byte, error) {
	if !i.Valid() {
		return nil, ErrInvalidInputRequest
	}
	return jsonv2.Marshal(inputRequestWire{
		Prompt: i.prompt, ResponseSchema: i.responseSchema.JSON(), ContinuationState: i.continuationState,
	})
}

func (i *InputRequest) UnmarshalJSON(data []byte) error {
	if i == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidInputRequest)
	}
	var wire inputRequestWire
	err := jsonv2.Unmarshal(data, &wire, jsonv2.RejectUnknownMembers(true))
	if err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidInputRequest, err)
	}
	schema, err := jsonschema.Parse(wire.ResponseSchema)
	if err != nil {
		return fmt.Errorf("%w: response schema: %w", ErrInvalidInputRequest, err)
	}
	request, err := NewInputRequest(InputRequestConfig{Prompt: wire.Prompt, ResponseSchema: schema, ContinuationState: wire.ContinuationState})
	if err != nil {
		return err
	}
	*i = request
	return nil
}

// Continuation validates an answer against this exact request and freezes the
// state and answer to pass explicitly to Contract.Prepare for the same call.
func (i InputRequest) Continuation(response json.RawMessage) (Continuation, error) {
	if !i.Valid() {
		return Continuation{}, ErrInvalidInputRequest
	}
	validated, err := validateInputResponse(i.responseSchema, response)
	if err != nil {
		return Continuation{}, err
	}
	return Continuation{state: bytes.Clone(i.continuationState), response: validated}, nil
}

func (i InputRequest) Prompt() json.RawMessage           { return bytes.Clone(i.prompt) }
func (i InputRequest) ResponseSchema() jsonschema.Schema { return i.responseSchema }
func (i InputRequest) State() json.RawMessage            { return bytes.Clone(i.continuationState) }

func (InputRequest) JSONSchemaAlias() any { return inputRequestWire{} }

// Continuation is the immutable checkpoint and validated answer for one logical
// call. Passing a context does not pass this business input.
type Continuation struct {
	state    json.RawMessage
	response json.RawMessage
}

func (c Continuation) State() json.RawMessage    { return bytes.Clone(c.state) }
func (c Continuation) Response() json.RawMessage { return bytes.Clone(c.response) }
func (c Continuation) valid() bool               { return len(c.state) > 0 && len(c.response) > 0 }

func validateInputResponse(schema jsonschema.Schema, response json.RawMessage) (json.RawMessage, error) {
	input, err := parseInputJSON(response)
	if err != nil {
		return nil, fmt.Errorf("%w: response: %w", ErrInvalidInputRequest, err)
	}
	if err := schema.Validate(input); err != nil {
		return nil, fmt.Errorf("%w: response: %w", ErrInvalidInputRequest, err)
	}
	return input, nil
}

func parseInputJSON(data json.RawMessage) (json.RawMessage, error) {
	if len(data) == 0 || len(data) > MaxInputBytes {
		return nil, fmt.Errorf("JSON value must contain at most %d bytes", MaxInputBytes)
	}
	value := jsontext.Value(bytes.Clone(data))
	if err := value.Format(jsontext.ReorderRawObjects(true), jsontext.EscapeForHTML(true), jsontext.EscapeForJS(true)); err != nil {
		return nil, err
	}
	if len(value) > MaxInputBytes {
		return nil, fmt.Errorf("normalized JSON value exceeds %d bytes", MaxInputBytes)
	}
	return json.RawMessage(value), nil
}
