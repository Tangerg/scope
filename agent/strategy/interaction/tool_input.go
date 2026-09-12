package interaction

import (
	"bytes"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
)

const maxInputProtocolBytes = 1 << 20

var (
	ErrInvalidToolInputRequest = errors.New("interaction: invalid tool input request")
	ErrToolInputRequired       = errors.New("interaction: tool input required")
)

// toolInputRequest freezes the checkpoint carried by RequireToolInput.
// The Tool owns continuation state; Engine owns Process and wait identities.
type toolInputRequest struct {
	prompt            json.RawMessage
	responseSchema    agent.Schema
	continuationState json.RawMessage
}

func newToolInputRequest(
	prompt json.RawMessage,
	responseSchema json.RawMessage,
	continuationState json.RawMessage,
) (toolInputRequest, error) {
	parsedPrompt, err := parseToolInputJSON(prompt)
	if err != nil {
		return toolInputRequest{}, fmt.Errorf("%w: prompt: %w", ErrInvalidToolInputRequest, err)
	}
	schema, err := agent.ParseSchema(responseSchema)
	if err != nil {
		return toolInputRequest{}, fmt.Errorf("%w: response schema: %w", ErrInvalidToolInputRequest, err)
	}
	continuation, err := parseToolInputJSON(continuationState)
	if err != nil {
		return toolInputRequest{}, fmt.Errorf("%w: continuation state: %w", ErrInvalidToolInputRequest, err)
	}
	return toolInputRequest{
		prompt: parsedPrompt.JSON(), responseSchema: schema, continuationState: continuation.JSON(),
	}, nil
}

func (t toolInputRequest) valid() bool {
	return len(t.prompt) > 0 && t.responseSchema.Valid() && len(t.continuationState) > 0
}

type toolInputRequestWire struct {
	Prompt            json.RawMessage `json:"prompt"`
	ResponseSchema    json.RawMessage `json:"response_schema"`
	ContinuationState json.RawMessage `json:"continuation_state"`
}

func (t toolInputRequest) MarshalJSON() ([]byte, error) {
	if !t.valid() {
		return nil, ErrInvalidToolInputRequest
	}
	return json.Marshal(toolInputRequestWire{
		Prompt: t.prompt, ResponseSchema: t.responseSchema.JSON(), ContinuationState: t.continuationState,
	})
}

func (t *toolInputRequest) UnmarshalJSON(data []byte) error {
	if t == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidToolInputRequest)
	}
	var wire toolInputRequestWire
	if err := jsonv2.Unmarshal(data, &wire, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidToolInputRequest, err)
	}
	request, err := newToolInputRequest(wire.Prompt, wire.ResponseSchema, wire.ContinuationState)
	if err != nil {
		return err
	}
	*t = request
	return nil
}

func (t toolInputRequest) equal(other toolInputRequest) bool {
	return bytes.Equal(t.prompt, other.prompt) &&
		bytes.Equal(t.responseSchema.JSON(), other.responseSchema.JSON()) &&
		bytes.Equal(t.continuationState, other.continuationState)
}

func (t toolInputRequest) validateResponse(response json.RawMessage) (json.RawMessage, error) {
	if !t.valid() {
		return nil, ErrInvalidToolInputRequest
	}
	return validateToolInputResponse(t.responseSchema, response)
}

func validateToolInputResponse(schema agent.Schema, response json.RawMessage) (json.RawMessage, error) {
	input, err := parseToolInputJSON(response)
	if err != nil {
		return nil, fmt.Errorf("%w: response: %w", ErrInvalidToolInputRequest, err)
	}
	if err := schema.ValidateInput(input); err != nil {
		return nil, fmt.Errorf("%w: response: %w", ErrInvalidToolInputRequest, err)
	}
	return input.JSON(), nil
}

// toolInputRequiredError carries one validated, snapshot-safe toolInputRequest across
// a Tool boundary. It is control flow, not a failed ToolResult.
type toolInputRequiredError struct {
	request toolInputRequest
}

// RequireToolInput validates the request and returns an error matching
// ErrToolInputRequired. A Tool returns this before external side effects, or after
// storing enough ContinuationState to prove safe re-entry. A HostFailure,
// cancellation, or deadline in the same error chain takes precedence: the Effect
// remains unknown instead of committing an input checkpoint.
//
// Prompt is a Tool-defined JSON value for the consumer; responseSchema governs
// its answer. ContinuationState is returned only to the requesting Tool through
// ToolInputContinuationFromContext. The request freezes all three values and
// holds no Process or wait identity. JSON numbers retain their precision; prompt
// and continuationState must each fit within one MiB before and after normalization.
// Use errors.Is with ErrToolInputRequired to classify this control outcome.
func RequireToolInput(
	prompt json.RawMessage,
	responseSchema json.RawMessage,
	continuationState json.RawMessage,
) error {
	request, err := newToolInputRequest(prompt, responseSchema, continuationState)
	if err != nil {
		return err
	}
	return &toolInputRequiredError{request: request}
}

func (*toolInputRequiredError) Error() string {
	return ErrToolInputRequired.Error()
}

func (*toolInputRequiredError) Unwrap() error { return ErrToolInputRequired }

func (t *toolInputRequiredError) inputRequest() (toolInputRequest, bool) {
	if t == nil || !t.request.valid() {
		return toolInputRequest{}, false
	}
	return t.request, true
}

// ToolInputContinuation is the immutable state and validated external response
// attached only while re-entering the Tool that requested input.
type ToolInputContinuation struct {
	state    json.RawMessage
	response json.RawMessage
}

// State returns the Tool-owned continuation state captured at suspension.
func (t ToolInputContinuation) State() json.RawMessage {
	return bytes.Clone(t.state)
}

// Response returns the schema-validated external input.
func (t ToolInputContinuation) Response() json.RawMessage {
	return bytes.Clone(t.response)
}

type toolContinuationContextKey struct{}

func withToolInputContinuation(ctx context.Context, continuation ToolInputContinuation) context.Context {
	return context.WithValue(ctx, toolContinuationContextKey{}, continuation)
}

// ToolInputContinuationFromContext returns continuation data only for the active
// resumed Tool call. Ordinary first attempts and a nil context return false.
func ToolInputContinuationFromContext(ctx context.Context) (ToolInputContinuation, bool) {
	if ctx == nil {
		return ToolInputContinuation{}, false
	}
	continuation, ok := ctx.Value(toolContinuationContextKey{}).(ToolInputContinuation)
	if !ok || len(continuation.state) == 0 || len(continuation.response) == 0 {
		return ToolInputContinuation{}, false
	}
	return continuation, true
}

// NewToolInputResponseSignal addresses an answer to the exact wait that asked
// for it. Requiring the wait identity is what prevents a late or duplicated
// response from satisfying a different pause than the one it was written for.
// It validates JSON only; the Execution checks the authoritative response
// schema. PendingToolInput.ResponseSignal composes local schema validation
// with this constructor.
func NewToolInputResponseSignal(
	id agent.SignalID,
	waitID agent.WaitID,
	response json.RawMessage,
) (agent.SignalRequest, error) {
	if !waitID.Valid() {
		return agent.SignalRequest{}, fmt.Errorf("%w: WaitID is required", ErrInvalidToolInputRequest)
	}
	input, err := parseToolInputJSON(response)
	if err != nil {
		return agent.SignalRequest{}, fmt.Errorf("%w: response: %w", ErrInvalidToolInputRequest, err)
	}
	payload, err := encodeProtocol(signalEnvelope{
		Operation:     operationInputResponse,
		InputResponse: input.JSON(),
	})
	if err != nil {
		return agent.SignalRequest{}, err
	}
	return agent.NewSignalRequest(id, waitID, payload)
}

func parseToolInputJSON(data json.RawMessage) (agent.Input, error) {
	if len(data) == 0 || len(data) > maxInputProtocolBytes {
		return agent.Input{}, fmt.Errorf("JSON value must contain at most %d bytes", maxInputProtocolBytes)
	}
	input, err := agent.ParseInput(data)
	if err != nil {
		return agent.Input{}, err
	}
	if len(input.JSON()) > maxInputProtocolBytes {
		return agent.Input{}, fmt.Errorf("normalized JSON value exceeds %d bytes", maxInputProtocolBytes)
	}
	return input, nil
}
