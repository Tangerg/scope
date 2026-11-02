package interaction

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	"github.com/Tangerg/scope/agent/internal/strategy/child"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

type callKind string

const (
	callTool            callKind = "tool"
	callDelegate        callKind = "delegate"
	toolInputWaitPrefix          = "tool.input."
)

// inputWait names the answer to one settled request. A continuation that asks
// again has another EffectID and therefore cannot accept a retry of an old answer.
type inputWait struct {
	Effect  run.EffectID      `json:"effect"`
	Request tool.InputRequest `json:"request"`
}

func (i inputWait) key() (run.WaitKey, error) {
	return run.ParseWaitKey(toolInputWaitPrefix + strconv.FormatUint(i.Effect.Step, 10) + "." + strconv.FormatUint(uint64(i.Effect.Index), 10))
}
func (i inputWait) wait() (run.Wait, error) {
	key, err := i.key()
	if err != nil {
		return run.Wait{}, err
	}
	question, err := run.ParsePayload(i.Request.Prompt())
	if err != nil {
		return run.Wait{}, err
	}
	return run.Wait{Key: key, Question: question, AnswerSchema: i.Request.ResponseSchema(), MaxAnswerBytes: tool.MaxInputBytes}, nil
}

type callInvocation struct {
	Reference run.EffectID      `json:"reference"`
	Pending   *run.EffectID     `json:"pending,omitzero"`
	Input     *inputWait        `json:"input,omitzero"`
	Child     *child.Invocation `json:"child,omitzero"`
	Result    *toolCallResult   `json:"result,omitzero"`
}

// obligation is the Effect whose settlement the invocation still awaits.
func (c callInvocation) obligation() run.EffectID {
	switch {
	case c.Pending != nil:
		return *c.Pending
	case c.Input != nil:
		return c.Input.Effect
	default:
		return run.EffectID{}
	}
}

// acceptDelegate applies the delegated child's messages; once the child is
// rejected or terminates, its outcome becomes the call's result.
func (c *callInvocation) acceptDelegate(call chat.ToolCall, message run.Message) (bool, error) {
	matched, err := c.Child.Accept(message)
	if err != nil || !matched {
		return matched, err
	}
	switch {
	case c.Child.Rejection != nil:
		c.Result = rejectedDelegateStartResult(call, *c.Child.Rejection)
		c.Child = nil
	case c.Child.Termination != nil:
		result, err := delegateToolResult(call, *c.Child.Termination)
		if err != nil {
			return false, err
		}
		c.Result = new(newToolCallResult(result))
		c.Child = nil
	}
	return true, nil
}

// acceptAnswer turns the answer to this call's open question into the Effect
// that resumes the tool under the call's original reference.
func (c *callInvocation) acceptAnswer(call chat.ToolCall, signal run.Signal, next run.EffectID) (run.Effect, bool, error) {
	if c.Input == nil {
		return run.Effect{}, false, nil
	}
	key, err := c.Input.key()
	if err != nil || signal.Wait != key {
		return run.Effect{}, false, err
	}
	effect, err := toolEffect(toolDispatchRequest{Invocation: toolCall{Call: call}, Resume: &toolResume{Reference: c.Reference, InputRequest: c.Input.Request, InputResponse: signal.Payload.JSON()}})
	if err != nil {
		return run.Effect{}, false, err
	}
	c.Pending = new(next)
	c.Input = nil
	return effect, true, nil
}

// acceptToolSettlement records the outcome of the call's pending Effect: a
// question, a result, or a host failure that ends the interaction.
func (c *callInvocation) acceptToolSettlement(call chat.ToolCall, completion run.Completion) (bool, *run.Failure, error) {
	if c.Pending == nil || completion.Effect != *c.Pending {
		return false, nil, nil
	}
	envelope, failure, err := decodeSettlement(completion.Outcome)
	if err != nil {
		return true, nil, err
	}
	if failure != "" {
		diagnostic, failureErr := run.NewFailure(run.FailureKindExternal, failureCodeInteractionHostFailed, failure)
		return true, &diagnostic, failureErr
	}
	if envelope.ToolResult == nil {
		return false, nil, ErrInvalidProtocol
	}
	outcome := envelope.ToolResult
	c.Pending = nil
	if outcome.InputRequest != nil {
		c.Input = &inputWait{Effect: completion.Effect, Request: *outcome.InputRequest}
		return true, nil, nil
	}
	if callErr := outcome.Completion.validateCall(call); callErr != nil {
		return false, nil, callErr
	}
	c.Result = outcome.Completion
	if outcome.AdvertisementFailure == "" {
		return true, nil, nil
	}
	advertisement, err := run.NewFailure(run.FailureKindContract, failureCodeInteractionToolAdvertisementInvalid, outcome.AdvertisementFailure)
	return true, &advertisement, err
}

// validate checks that the invocation is in exactly one state and that the
// state is legal for the batch's kind of call.
func (c callInvocation) validate(kind callKind, call chat.ToolCall) error {
	if !c.Reference.Valid() || c.states() != 1 {
		return ErrInvalidExecutionState
	}
	switch {
	case c.Pending != nil:
		return c.validateToolObligation(kind, *c.Pending, true)
	case c.Input != nil:
		return c.validateToolObligation(kind, c.Input.Effect, c.Input.Request.Valid())
	case c.Child != nil:
		return c.validateChild(kind)
	default:
		if err := c.Result.validateCall(call); err != nil {
			return fmt.Errorf("%w: call result: %w", ErrInvalidExecutionState, err)
		}
		return nil
	}
}

func (c callInvocation) states() int {
	count := 0
	for _, present := range []bool{c.Pending != nil, c.Input != nil, c.Child != nil, c.Result != nil} {
		if present {
			count++
		}
	}
	return count
}

func (c callInvocation) validateToolObligation(kind callKind, effect run.EffectID, valid bool) error {
	if kind != callTool || !valid || !effect.Valid() || !c.followsReference(effect) {
		return ErrInvalidExecutionState
	}
	return nil
}

// validateChild requires an unfinished child started by this call's own Spawn.
func (c callInvocation) validateChild(kind callKind) error {
	if kind != callDelegate || !c.Child.Valid() || c.Child.Done() || c.Child.Request != nil && *c.Child.Request != c.Reference {
		return ErrInvalidExecutionState
	}
	if c.Child.Process == nil {
		return nil
	}
	if parent, _ := c.Child.Process.Parent(); parent != c.Reference.Process {
		return ErrInvalidExecutionState
	}
	return nil
}

func (c callInvocation) followsReference(effect run.EffectID) bool {
	return effect.Process == c.Reference.Process && (effect == c.Reference || effect.Step > c.Reference.Step)
}

func (c callInvocation) validateDelegate(call chat.ToolCall, sequence uint64, index uint32, delegate Delegate) error {
	if c.Reference.Index != index {
		return ErrInvalidExecutionState
	}
	if c.Result != nil {
		if c.Result.Disposition == ResultSucceeded {
			if _, err := delegate.admitResultOutput(c.Result.Output); err != nil {
				return fmt.Errorf("%w: delegated result: %w", ErrInvalidExecutionState, err)
			}
		}
		return nil
	}
	if c.Child.Process != nil {
		expected, err := DelegateChildKey(sequence, call)
		if err != nil {
			return fmt.Errorf("%w: delegated identity: %w", ErrInvalidExecutionState, err)
		}
		actual, _ := c.Child.Process.Key()
		if actual != expected {
			return ErrInvalidExecutionState
		}
	}
	return nil
}

type callBatch struct {
	Kind        callKind          `json:"kind"`
	Invocations []*callInvocation `json:"invocations"`
}

func (c callBatch) phase() phase { return phaseAwaitingCalls }
func (c callBatch) active() int {
	count := 0
	for _, invocation := range c.Invocations {
		if invocation != nil && invocation.Result == nil {
			count++
		}
	}
	return count
}

// validate checks the batch's own invariants: started invocations form a prefix
// in declaration order, and no Effect is owned by two invocations.
func (c callBatch) validate(ctx context.Context, calls []chat.ToolCall) error {
	if c.Kind != callTool && c.Kind != callDelegate || len(c.Invocations) == 0 || len(c.Invocations) != len(calls) {
		return ErrInvalidExecutionState
	}
	owners := make(map[run.EffectID]int, 2*len(c.Invocations))
	started := c.started()
	for index, invocation := range c.Invocations[:started] {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := invocation.validate(c.Kind, calls[index]); err != nil {
			return err
		}
		if index > 0 && !c.Invocations[index-1].Reference.Precedes(invocation.Reference) {
			return ErrInvalidExecutionState
		}
		if !claimEffect(owners, invocation.Reference, index) || !claimEffect(owners, invocation.obligation(), index) {
			return ErrInvalidExecutionState
		}
	}
	unstarted := len(c.Invocations) - started
	if slices.ContainsFunc(c.Invocations[started:], func(invocation *callInvocation) bool { return invocation != nil }) ||
		c.Kind == callDelegate && unstarted > 0 || c.active() == 0 {
		return ErrInvalidExecutionState
	}
	return nil
}

// started is the length of the prefix of invocations that have started.
func (c callBatch) started() int {
	for index, invocation := range c.Invocations {
		if invocation == nil {
			return index
		}
	}
	return len(c.Invocations)
}

// claimEffect records owner for effect. An invocation may name the same Effect
// as both reference and obligation, but no Effect belongs to two invocations.
func claimEffect(owners map[run.EffectID]int, effect run.EffectID, owner int) bool {
	if !effect.Valid() {
		return true
	}
	if existing, claimed := owners[effect]; claimed {
		return existing == owner
	}
	owners[effect] = owner
	return true
}

func (c callBatch) validateBindings(ctx context.Context, definition *definition, calls, remaining []chat.ToolCall, sequence uint64) error {
	for index, call := range calls {
		if err := ctx.Err(); err != nil {
			return err
		}
		delegate, delegated := definition.delegate(call.Name)
		if delegated != (c.Kind == callDelegate) {
			return ErrInvalidExecutionState
		}
		if delegated {
			invocation := c.Invocations[index]
			if invocation.Reference.Step != c.Invocations[0].Reference.Step {
				return ErrInvalidExecutionState
			}
			if err := invocation.validateDelegate(call, sequence, uint32(index), delegate); err != nil {
				return err
			}
			continue
		}
		if _, found := definition.tools.entries[call.Name]; !found {
			return ErrInvalidExecutionState
		}
	}
	if c.Kind == callDelegate {
		return nil
	}
	count, err := definition.toolWindow(ctx, remaining)
	if err != nil {
		return err
	}
	if count != len(c.Invocations) || c.active() > definition.maxConcurrentToolCalls {
		return ErrInvalidExecutionState
	}
	for _, invocation := range c.Invocations {
		if invocation != nil && invocation.Result != nil {
			if err := definition.tools.validateAdvertisements(invocation.Result.AdvertisedToolNames); err != nil {
				return fmt.Errorf("%w: call advertisements: %w", ErrInvalidExecutionState, err)
			}
		}
	}
	return nil
}
func (c callBatch) waits() ([]run.Wait, error) {
	var waits []run.Wait
	for _, invocation := range c.Invocations {
		if invocation == nil || invocation.Input == nil {
			continue
		}
		wait, err := invocation.Input.wait()
		if err != nil {
			return nil, fmt.Errorf("%w: tool input: %w", ErrInvalidExecutionState, err)
		}
		waits = append(waits, wait)
	}
	return waits, nil
}
