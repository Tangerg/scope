// Package interaction provides the model-directed execution Strategy for the
// Agent Framework.
//
// A Definition owns the serializable working context, bounded model/Tool state
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
// SignalRequest through agent.SignalChild without another steering protocol.
//
// Only FinishReasonToolCalls admits Tool and Delegate execution. Length-truncated
// calls receive model-visible feedback for another bounded model attempt; calls
// accompanying other finish reasons fail the Process without execution. Restored
// pending batches must satisfy the same admission rule. Advertised names must
// refer to bound deferred Tools, including during restoration. Model preparation
// errors settle as definite host failures before external work begins.
//
// Every known Tool and Delegate result, including validation, availability,
// truncation, authorization, and child-admission rejections, crosses one
// publication Effect per model response. ResultCommitter receives the complete
// ordered call set and exact outputs. Scope adopts them only after verifying
// its ResultReceipt. Tool execution and result publication have separate Effect
// identities: a lost publication receipt cannot turn a known result into an
// unknown execution or cause the Tool to run again. Pending snapshots retain
// the exact results. Nil ResultCommitter acknowledges in memory only; durable
// recovery additionally requires the Engine's TreeDurability and host storage.
// ResultCommitter must reconcile and publish idempotently under the original
// EffectID, with current-writer fencing. Pending publication can replay on
// restore; settled Unknown publication requires Process.ReplayUnknownEffect or
// ResolveUnknownEffect with a verified stored receipt. A fresh Dispatcher
// reconstructs ResultBatch from the persisted intent, without rerunning Tools
// or reading old host memory. If the transaction remains uncertain, adoption
// stays blocked. Receipt validation binds both content and settlement identity.
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
package interaction
