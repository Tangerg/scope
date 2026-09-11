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
// An ordinary Tool error produces a model-visible ToolResult. Host failures,
// cancellation, deadlines, and panics that produce no definite ToolResult leave
// the Tool Effect unknown. The Engine retains that identity across tree capture
// and restoration and requires explicit settlement before execution continues.
// Terminating the Process retains unresolved identities in its Result; it does
// not establish that external work failed. Observer callbacks describe attempts
// and never replace the Engine's authoritative settlement boundary. When a Tool
// child terminates, its Failure propagates unchanged to the parent. Cancellation
// and timeout diagnostics retain the child identity and termination cause.
package interaction
