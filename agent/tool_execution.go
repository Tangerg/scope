package agent

import (
	"context"
	jsonv2 "encoding/json/v2"
	"fmt"

	"github.com/Tangerg/scope/core/tool"

	"github.com/Tangerg/scope/agent/internal/strategy/restore"
	"github.com/Tangerg/scope/agent/internal/strategy/stepfail"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

const toolExecutionStateKind = "interaction.tool_call"

// toolInputWaitKey names the one input wait a Tool child holds open at a time.
// Input waits are sequential: a Step consumes the previous wait's answer before
// it requests the next, so the mailbox never sees this key open twice and the
// wait never needs a per-pause identity.
const toolInputWaitKey = "interaction.tool_input"

type toolPhase string

const (
	toolReady            toolPhase = "ready"
	toolAwaitingResult   toolPhase = "awaiting_result"
	toolAwaitingWaitOpen toolPhase = "awaiting_wait_open"
	toolWaitingInput     toolPhase = "waiting_input"
	toolCompleted        toolPhase = "completed"
)

type toolExecutionState struct {
	Phase toolPhase `json:"phase"`
	Call  toolCall  `json:"call"`
	// InputRequest is the input the Tool is currently waiting on. It exists only
	// while the Tool awaits input and is cleared once copied into the resume
	// Effect, so no stale request outlives the wait it belongs to.
	InputRequest *tool.InputRequest `json:"input_request,omitzero"`
	WaitID       *run.WaitID        `json:"wait_id,omitzero"`
}

func (t toolExecutionState) phase() toolPhase {
	if t.Phase == toolAwaitingWaitOpen && t.WaitID != nil {
		return toolWaitingInput
	}
	return t.Phase
}

func (t toolExecutionState) validate() error {
	if err := t.Call.validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
	}
	if t.InputRequest != nil && !t.InputRequest.Valid() {
		return fmt.Errorf("%w: %w", ErrInvalidExecutionState, tool.ErrInvalidInputRequest)
	}
	if !t.continuationMatchesPhase() {
		return fmt.Errorf("%w: Tool phase disagrees with its continuation", ErrInvalidExecutionState)
	}
	return nil
}

func (t toolExecutionState) continuationMatchesPhase() bool {
	if t.WaitID != nil && (t.Phase != toolAwaitingWaitOpen || !t.WaitID.Valid()) {
		return false
	}
	switch t.Phase {
	case toolReady, toolCompleted, toolAwaitingResult:
		return t.InputRequest == nil
	case toolAwaitingWaitOpen:
		return t.InputRequest != nil
	default:
		return false
	}
}

type toolDefinition struct{ descriptor run.Descriptor }

func newToolDefinition(name, description string) (*toolDefinition, error) {
	inputSchema, err := jsonschema.For[toolCall]()
	if err != nil {
		return nil, err
	}
	outputSchema, err := jsonschema.For[toolCallResult]()
	if err != nil {
		return nil, err
	}
	descriptor, err := run.NewDescriptor(run.DescriptorConfig{
		Name: name, Description: description, InputSchema: inputSchema, OutputSchema: outputSchema,
	})
	if err != nil {
		return nil, err
	}
	return &toolDefinition{descriptor: descriptor}, nil
}

func (t *toolDefinition) Descriptor() run.Descriptor { return t.descriptor }

func (*toolDefinition) ChildPrograms() []run.Program[run.Payload, run.Payload] { return nil }

func (t *toolDefinition) Start(input run.Payload) (run.Execution, error) {
	call, err := input.Decode[toolCall]()
	if err != nil {
		return nil, fmt.Errorf("%w: Tool input: %w", ErrInvalidInput, err)
	}
	state := toolExecutionState{Phase: toolReady, Call: call}
	if err := state.validate(); err != nil {
		return nil, err
	}
	return &toolExecution{state: state}, nil
}

func (t *toolDefinition) Restore(ctx context.Context, state run.ExecutionState) (run.Execution, error) {
	decoded, err := restore.Decode(ctx, state, toolExecutionStateKind, ErrInvalidExecutionState, validateToolState)
	if err != nil {
		return nil, err
	}
	return &toolExecution{state: decoded}, nil
}

// decodeToolState reads a retained child state outside Restore; it applies
// the same decoding and validation without a cancellation point.
func decodeToolState(state run.ExecutionState) (toolExecutionState, error) {
	return restore.Decode(context.Background(), state, toolExecutionStateKind, ErrInvalidExecutionState, validateToolState)
}

func validateToolState(_ context.Context, state toolExecutionState) error { return state.validate() }

type toolExecution struct{ state toolExecutionState }

func (t *toolExecution) Snapshot() (run.ExecutionState, error) {
	return run.EncodeExecutionState(toolExecutionStateKind, t.state)
}

func (t *toolExecution) Step(ctx context.Context, signals []run.Signal) (run.Transition, error) {
	if err := ctx.Err(); err != nil {
		return run.Transition{}, err
	}
	if t.state.Phase == toolReady {
		if len(signals) != 0 {
			return run.Transition{}, ErrInvalidExecutionState
		}
		return t.request(0, toolDispatchRequest{Invocation: t.state.Call})
	}
	if len(signals) == 0 {
		return run.Transition{}, fmt.Errorf("%w: Tool expected a Signal", ErrInvalidExecutionState)
	}
	signal := signals[0]
	if t.state.phase() == toolAwaitingWaitOpen {
		return t.acceptWaitOpened(signal)
	}
	switch t.state.phase() {
	case toolAwaitingResult:
		settlement, err := signal.Frame[run.Settlement]()
		if err != nil {
			return run.Transition{}, fmt.Errorf("%w: %w", ErrInvalidProtocol, err)
		}
		envelope, hostFailure, err := decodeSettlement(settlement)
		if err != nil {
			return run.Transition{}, err
		}
		if hostFailure != "" {
			return stepfail.Transition(1, run.FailureKindExternal, failureCodeInteractionHostFailed, hostFailure)
		}
		return t.acceptResult(envelope)
	case toolWaitingInput:
		return t.acceptInputResponse(signal)
	default:
		return run.Transition{}, ErrInvalidExecutionState
	}
}

func (t *toolExecution) acceptWaitOpened(signal run.Signal) (run.Transition, error) {
	opening, err := signal.Frame[run.WaitOpened]()
	if err != nil {
		return run.Transition{}, fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
	}
	waitID := opening.WaitID()
	t.state.WaitID = &waitID
	return run.Wait(1, waitID)
}

func (t *toolExecution) acceptInputResponse(signal run.Signal) (run.Transition, error) {
	waitID, addressed := signal.WaitID()
	input, err := signal.Frame[run.ExternalInput]()
	if err != nil || !addressed || waitID != *t.state.WaitID {
		return run.Transition{}, ErrInvalidExecutionState
	}
	return t.request(1, toolDispatchRequest{
		Invocation: t.state.Call,
		Resume:     &toolResume{InputRequest: *t.state.InputRequest, InputResponse: input.Payload()},
	})
}

func (t *toolExecution) request(consumed uint32, call toolDispatchRequest) (run.Transition, error) {
	envelope, err := newToolEffect(call)
	if err != nil {
		return run.Transition{}, err
	}
	payload, err := jsonv2.Marshal(envelope, jsonv2.Deterministic(true))
	if err != nil {
		return run.Transition{}, err
	}
	effect, err := run.NewDispatcherEffect(payload)
	if err != nil {
		return run.Transition{}, err
	}
	t.state.InputRequest = nil
	t.state.WaitID = nil
	t.state.Phase = toolAwaitingResult
	return run.Continue(consumed, effect)
}

func (t *toolExecution) acceptResult(envelope signalEnvelope) (run.Transition, error) {
	if envelope.operation() != operationToolCall {
		return run.Transition{}, ErrInvalidExecutionState
	}
	outcome := envelope.ToolResult
	if outcome.InputRequest != nil {
		return t.openInputWait(*outcome.InputRequest)
	}
	result := outcome.Completion
	if err := result.validateCall(t.state.Call.Call); err != nil {
		return run.Transition{}, fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
	}
	if outcome.AdvertisementFailure != "" {
		// Failed termination has no output owner. The mailbox must retain the
		// completed call for recovery and result projection instead of consuming it.
		return stepfail.Transition(0, run.FailureKindContract, failureCodeInteractionToolAdvertisementInvalid, outcome.AdvertisementFailure)
	}
	output, err := run.EncodePayload(result)
	if err != nil {
		return run.Transition{}, err
	}
	t.state.InputRequest = nil
	t.state.WaitID = nil
	t.state.Phase = toolCompleted
	return run.Complete(1, output)
}

func (t *toolExecution) openInputWait(request tool.InputRequest) (run.Transition, error) {
	key, err := run.ParseWaitKey(toolInputWaitKey)
	if err != nil {
		return run.Transition{}, err
	}
	// The mailbox admits the same response the continuation consumes. Keeping
	// its Schema intact preserves local references, anchors and resource IDs.
	spec, err := run.NewExternalWaitSpec(run.ExternalWaitSpecConfig{
		Key: key, Question: request.Prompt(), AnswerSchema: request.ResponseSchema(), MaxAnswerBytes: tool.MaxInputBytes,
	})
	if err != nil {
		return run.Transition{}, err
	}
	effect, err := run.NewWaitEffect(spec)
	if err != nil {
		return run.Transition{}, err
	}
	t.state.InputRequest = &request
	t.state.Phase = toolAwaitingWaitOpen
	return run.Continue(1, effect)
}

var _ run.Definition = (*toolDefinition)(nil)

const failureCodeInteractionToolAdvertisementInvalid = "interaction.tool.advertisement_invalid"
