package interaction

import (
	"fmt"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

// ToolInvocation is the durable identity of one tool call Effect. A resumed
// call keeps the identity of the Effect that first requested input, so every
// continuation of one logical call shares its idempotency key.
type ToolInvocation struct {
	request run.EffectRequest
	call    toolDispatchRequest
}

func ToolInvocationFromRequest(request run.EffectRequest) (ToolInvocation, error) {
	if !request.Valid() {
		return ToolInvocation{}, ErrInvalidProtocol
	}
	envelope, err := decodeEffect(request.Effect().Payload().JSON())
	if err != nil {
		return ToolInvocation{}, err
	}
	if envelope.ToolCall == nil {
		return ToolInvocation{}, fmt.Errorf("%w: tool attribution requires a tool effect", ErrInvalidProtocol)
	}
	invocation := ToolInvocation{request: request, call: *envelope.ToolCall}
	if !invocation.Valid() {
		return ToolInvocation{}, ErrInvalidProtocol
	}
	return invocation, nil
}

func (t ToolInvocation) ProcessID() run.ProcessID   { return t.request.ID().Process }
func (t ToolInvocation) Epoch() run.Epoch           { return t.request.Epoch() }
func (t ToolInvocation) ProgramRef() run.ProgramRef { return t.request.ProgramRef() }
func (t ToolInvocation) EffectID() run.EffectID     { return t.request.ID() }
func (t ToolInvocation) ToolCall() chat.ToolCall    { return t.call.Invocation.Call }

// Reference is the Effect that started the logical call.
func (t ToolInvocation) Reference() run.EffectID {
	if t.call.Resume != nil {
		return t.call.Resume.Reference
	}
	return t.request.ID()
}

// IdempotencyKey is the key the Tool receives in its Admission. Hosts use it to
// find the external record of a call whose outcome the runtime cannot know.
// It digests the call's Reference so a Tool forwarding it to a third party
// discloses no process path or step.
func (t ToolInvocation) IdempotencyKey() tool.IdempotencyKey { return idempotencyKey(t.Reference()) }

func idempotencyKey(reference run.EffectID) tool.IdempotencyKey {
	return tool.IdempotencyKey(run.ComputeDigest([]byte(reference.String())).String())
}

func (t ToolInvocation) Valid() bool {
	return t.request.Valid() && t.call.Invocation.validate() == nil && (t.call.Resume == nil || t.call.Resume.precedes(t.request.ID()))
}

func (t ToolInvocation) admission() (tool.Admission, error) {
	admission := tool.Admission{IdempotencyKey: t.IdempotencyKey()}
	if t.call.Resume == nil {
		return admission, nil
	}
	continuation, err := t.call.Resume.InputRequest.Continuation(t.call.Resume.InputResponse)
	if err != nil {
		return tool.Admission{}, err
	}
	admission.Continuation = &continuation
	return admission, nil
}
