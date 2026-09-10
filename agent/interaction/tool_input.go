package interaction

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
)

const maxInputProtocolBytes = 1 << 20

var (
	ErrInvalidToolInputRequest = errors.New("interaction: invalid tool input request")
	ErrToolInputRequired       = errors.New("interaction: tool input required")
)

// ToolInputRequest is an immutable Tool request for external input. Prompt is an
// owner-defined JSON value for a consumer, ResponseSchema is authoritative,
// and ContinuationState is returned only to the same Tool when input arrives.
// It contains no Process identity or WaitID; Engine owns those identities.
type ToolInputRequest struct {
	prompt            json.RawMessage
	responseSchema    agent.Schema
	continuationState json.RawMessage
}

// NewToolInputRequest lets a tool pause for external input while carrying its
// own continuation state. The response schema is validated here so an answer
// can be checked when it arrives; the request holds no Process or wait
// identity, because those are minted by the Engine and would otherwise be
// forgeable by a tool. JSON numbers retain their precision; each JSON value
// must fit within one MiB before and after normalization.
func NewToolInputRequest(
	prompt json.RawMessage,
	responseSchema json.RawMessage,
	continuationState json.RawMessage,
) (ToolInputRequest, error) {
	parsedPrompt, err := parseToolInputJSON(prompt)
	if err != nil {
		return ToolInputRequest{}, fmt.Errorf("%w: prompt: %w", ErrInvalidToolInputRequest, err)
	}
	schema, err := agent.ParseSchema(responseSchema)
	if err != nil {
		return ToolInputRequest{}, fmt.Errorf("%w: response schema: %w", ErrInvalidToolInputRequest, err)
	}
	continuation, err := parseToolInputJSON(continuationState)
	if err != nil {
		return ToolInputRequest{}, fmt.Errorf("%w: continuation state: %w", ErrInvalidToolInputRequest, err)
	}
	return ToolInputRequest{
		prompt: parsedPrompt.JSON(), responseSchema: schema, continuationState: continuation.JSON(),
	}, nil
}

// Prompt returns an independently owned consumer-facing JSON value.
func (t ToolInputRequest) Prompt() json.RawMessage { return bytes.Clone(t.prompt) }

// ResponseSchema returns the authoritative JSON Schema for an answer.
func (t ToolInputRequest) ResponseSchema() json.RawMessage {
	return t.responseSchema.JSON()
}

// ContinuationState returns opaque state owned by the requesting Tool.
func (t ToolInputRequest) ContinuationState() json.RawMessage {
	return bytes.Clone(t.continuationState)
}

func (t ToolInputRequest) Valid() bool {
	return len(t.prompt) > 0 && t.responseSchema.Valid() && len(t.continuationState) > 0
}

func (t ToolInputRequest) validateResponse(response json.RawMessage) (json.RawMessage, error) {
	if !t.Valid() {
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

// ToolInputRequiredError carries one validated, snapshot-safe ToolInputRequest across
// a Tool boundary. It is control flow, not a failed ToolResult.
type ToolInputRequiredError struct {
	request ToolInputRequest
}

// RequireToolInput validates the request and returns an error matching
// ErrToolInputRequired. A Tool returns this before external side effects, or after
// storing enough ContinuationState to prove safe re-entry. A HostFailure,
// cancellation, or deadline in the same error chain takes precedence: the Effect
// remains unknown instead of committing an input checkpoint.
func RequireToolInput(
	prompt json.RawMessage,
	responseSchema json.RawMessage,
	continuationState json.RawMessage,
) error {
	request, err := NewToolInputRequest(prompt, responseSchema, continuationState)
	if err != nil {
		return err
	}
	return &ToolInputRequiredError{request: request}
}

func (*ToolInputRequiredError) Error() string {
	return ErrToolInputRequired.Error()
}

func (*ToolInputRequiredError) Unwrap() error { return ErrToolInputRequired }

func (t *ToolInputRequiredError) inputRequest() (ToolInputRequest, bool) {
	if t == nil || !t.request.Valid() {
		return ToolInputRequest{}, false
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
	return ToolInputContinuation{
		state: bytes.Clone(continuation.state), response: bytes.Clone(continuation.response),
	}, true
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
