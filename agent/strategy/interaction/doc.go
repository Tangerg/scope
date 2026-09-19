// Package interaction provides the model-directed execution Strategy for the
// Agent Framework.
//
// Input and Output reuse core/chat values for this Strategy; custom Strategies
// may define their own payload shapes within agent.Payload validation limits.
//
// Model-call and child work quotas default to unlimited. Host cancellation and
// independently configured concurrency and mailbox capacity remain effective.
// A finite zero MaxModelCalls is rejected at construction. Invalid provider
// output terminates with external / interaction.model.invalid_response, without
// retrying the model or committing the rejected Step.
//
// A Definition owns the serializable working context, model/Tool state
// machine, exact managed Delegate bindings, typed Delegate Artifacts, and an
// optional pure completion validator. A Dispatcher owns model I/O. A ToolSet
// binds ordinary executable Tools to a separate Deployment; the Engine must
// resolve that exact child binding. Definition allocates an explicit budget to
// each Tool child and schedules calls within their declared concurrency bounds.
// ToolSet freezes independent input validators and scheduling classifiers;
// Definition retains these pure declarations without retaining executable Tools
// or their backends. Hosts cover declaration identity in deployment digests.
// Each call owns one Effect, its result, and any input continuation. Completed
// siblings retain their settlements when another call remains unknown or waits
// for input. Model context receives the complete results in original call order.
// A Tool child's input describes only its initial invocation, and its output
// describes only completion. Resumption belongs to the child's dispatcher
// protocol; its checkpoint retains the validated input request and Tool-owned continuation.
//
// Interaction requests ordinary Tool and Delegate children only through
// Framework Effects. One child-call batch owns start confirmations, wait
// identity, boundary validation, and ordered results for both bindings. Tools
// refill their bounded window after any child drains; Delegates await their
// entire batch. Execution snapshots retain this single batch and its policy.
// A rejected ordinary Tool child start terminates the Interaction with the
// original Failure: the ToolSet is required execution infrastructure. A rejected
// Delegate start is instead a model-visible rejected result, because choosing
// another advertised worker is part of the model's delegation policy.
// Engine owns child Process lifecycles. Product conversation
// history, persistence, application artifact stores, pricing, approval policy,
// and UI remain outside this Strategy. Direct model calls remain available
// through package chatclient without constructing an Interaction or Engine.
//
// [PendingToolInputs] reads current Tool waits from one TreeSnapshot. A response
// is sent to the returned PendingToolInput.ProcessID, because that child owns
// its WaitID and continuation independently of its parent and siblings.
//
// [NewSteerSignal] supplies additional user messages through the ordinary
// mailbox. Steering accepted during model or child work applies at the next
// safe model boundary after the current result batch; it does not preempt a
// model request or answer a Tool input wait. A parent can deliver the same
// SignalRequest through agent.NewChildSignalEffect without another steering protocol.
//
// Only FinishReasonToolCalls admits Tool and Delegate execution. Length-truncated
// calls receive model-visible feedback for another model attempt; calls
// accompanying other finish reasons fail the Process without execution. Restored
// pending batches must satisfy the same admission rule. Advertised names must
// refer to bound deferred Tools, including during restoration. Model preparation
// errors settle as definite host failures before external work begins.
//
// SettledResults interprets exact known Tool and Delegate outcomes from the
// authoritative tree, including sparse and cancellation-drained settlements.
// Its RoundResults view is read-only and has no storage or acknowledgment role.
// Hosts that need a separate result history can derive it atomically within their
// TreeDurability transactions; its storage, retention, and delivery remain host policy.
// Local rejections and complete rounds cross explicit Checkpoint transitions.
// Only a complete round can cross that boundary into model continuation or direct
// completion; durable execution also waits for storage acknowledgment. A canceled
// parent never resumes to collect or publish children.
//
// Await fixes the Process terminal; Join additionally drains descendant calls
// and their required storage acknowledgments. Canceling either caller wait does
// not discard owned work. TreeDurability hosts own bounded storage operations and
// an explicit host-release cancellation path. Storage failure stops this runtime
// with RuntimeError; it does not manufacture an unknown ToolResult. An
// acknowledged or reconciled transaction retains exact execution facts. Recovery
// loads that authoritative transaction and activates a fenced writer. Ephemeral
// engines provide no durable recovery. Scope-owned state uses one strict current
// schema; retired result_commit Effects and awaiting_result_commit checkpoints
// are rejected.
//
// A valid tool.Failure produces its complete model-visible error ToolResult and
// rejection disposition. Its diagnostic Cause cannot issue cancellation, input,
// or host-control signals; an outer HostFailure still declares that no definite
// ToolResult is available. Guard authorization errors do not carry a decision or
// expose the policy's internal outcome. Ordinary errors,
// invalid output, cancellation, deadlines, and panics without a definite result leave
// the Tool Effect unknown. The Engine retains that identity across tree capture
// and restoration and requires explicit settlement before execution continues.
// Terminating the Process retains unresolved identities in its Result; it does
// not establish that external work failed. Observer callbacks describe attempts
// and never replace the Engine's authoritative settlement boundary. When a Tool
// child terminates, its Failure propagates unchanged to the parent. Cancellation
// and timeout diagnostics retain the child identity and termination cause.
// A drained Delegate subtree with unresolved Effects fails the parent without
// another model call. ChildOutcome preserves their owning ProcessID and EffectID. Drained child work does not prove a definite external outcome.
//
// Descriptor.SignalSchema admits only steering envelopes as unaddressed input.
// Steering queues during child work and is consumed with protocol frames at
// the next safe Step, then applied before the next model call. Tool answers
// address the Tool child's wait. Unsupported envelopes are rejected before
// admission; semantic protocol violations discard the Step with a contract
// Failure. A candidate never consumes input on a Step error.
package interaction
