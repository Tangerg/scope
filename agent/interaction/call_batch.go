package interaction

import (
	"context"
	"fmt"
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

func (c callInvocation) validate(kind callKind, call chat.ToolCall) error {
	if !c.Reference.Valid() {
		return ErrInvalidExecutionState
	}
	variants := 0
	if c.Pending != nil {
		variants++
		if kind != callTool || !c.Pending.Valid() || !c.followsReference(*c.Pending) {
			return ErrInvalidExecutionState
		}
	}
	if c.Input != nil {
		variants++
		if kind != callTool || !c.Input.Effect.Valid() || !c.followsReference(c.Input.Effect) || !c.Input.Request.Valid() {
			return ErrInvalidExecutionState
		}
	}
	if c.Child != nil {
		variants++
		if kind != callDelegate || !c.Child.Valid() || c.Child.Done() || c.Child.Request != nil && *c.Child.Request != c.Reference {
			return ErrInvalidExecutionState
		}
		if c.Child.Process != nil {
			parent, _ := c.Child.Process.Parent()
			if parent != c.Reference.Process {
				return ErrInvalidExecutionState
			}
		}
	}
	if c.Result != nil {
		variants++
		if err := c.Result.validateCall(call); err != nil {
			return fmt.Errorf("%w: call result: %w", ErrInvalidExecutionState, err)
		}
	}
	if variants != 1 {
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
func (c callBatch) validate(ctx context.Context, calls []chat.ToolCall) error {
	if c.Kind != callTool && c.Kind != callDelegate || len(c.Invocations) == 0 || len(c.Invocations) != len(calls) {
		return ErrInvalidExecutionState
	}
	references := make(map[run.EffectID]int, len(c.Invocations))
	obligations := make(map[run.EffectID]int, len(c.Invocations))
	unstarted := false
	var previous run.EffectID
	for index, invocation := range c.Invocations {
		if err := ctx.Err(); err != nil {
			return err
		}
		if invocation == nil {
			if c.Kind == callDelegate {
				return ErrInvalidExecutionState
			}
			unstarted = true
			continue
		}
		if unstarted {
			return ErrInvalidExecutionState
		}
		if err := invocation.validate(c.Kind, calls[index]); err != nil {
			return err
		}
		reference := invocation.Reference
		if previous.Valid() && (reference.Process != previous.Process || reference.Step < previous.Step ||
			reference.Step == previous.Step && reference.Index <= previous.Index) {
			return ErrInvalidExecutionState
		}
		previous = reference
		references[reference] = index
		var obligation run.EffectID
		switch {
		case invocation.Pending != nil:
			obligation = *invocation.Pending
		case invocation.Input != nil:
			obligation = invocation.Input.Effect
		}
		if obligation.Valid() {
			if _, duplicate := obligations[obligation]; duplicate {
				return ErrInvalidExecutionState
			}
			obligations[obligation] = index
		}
	}
	for effect, invocation := range obligations {
		if owner, found := references[effect]; found && owner != invocation {
			return ErrInvalidExecutionState
		}
	}
	if c.active() == 0 {
		return ErrInvalidExecutionState
	}

	return nil
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
