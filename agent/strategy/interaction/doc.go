// Package interaction provides the model-directed execution Strategy for the
// Agent Framework: the reusable model-and-tool loop that model-driven roles
// configure rather than reimplement, and that higher strategies compose through
// exact Deployments and child Processes rather than copy. Input and Output reuse
// core/chat values.
//
// A Definition binds immutable model/Tool behavior, exact managed Delegates,
// and an optional pure completion validator. Each execution owns its
// serializable working context and state-machine progress. A Dispatcher owns
// model I/O. A ToolSet binds ordinary executable Tools to a separate child
// Deployment that the Definition binds; its scheduling retains only frozen
// input validators and classifiers, never executable Tools or their backends.
// Working context and model-call Effects carry only messages and options;
// the Dispatcher projects executable Tool definitions from the Deployment.
// Persisted requests that declare tools or tool_choice are rejected.
// Hosts cover declaration identity in deployment digests. Direct model calls
// remain available through package chatclient without an Interaction or Engine.
//
// Model-call and child work quotas default to unlimited; a finite zero
// MaxModelCalls is rejected at construction. Model boundary errors, including
// nil or structurally invalid responses, leave the Effect Unknown and require
// explicit Host resolution; an error that wraps [chat.ErrDefiniteFailure], such
// as a safeguard input or output rejection, instead settles a definite failure. A
// structurally valid response that violates
// Interaction constraints terminates with external /
// interaction.model.invalid_response without retrying the model or committing
// the rejected Step. Model preparation errors settle as definite host failures
// before external work begins.
//
// Only FinishReasonToolCalls admits Tool and Delegate execution.
// Length-truncated calls receive model-visible feedback for another model
// attempt; calls accompanying other finish reasons fail the Process without
// execution. Restored pending batches satisfy the same rule, and advertised
// names must refer to bound deferred Tools.
//
// Tool and Delegate children are requested only through Framework Effects. One
// child-call batch owns start confirmations, wait identity, boundary
// validation, and ordered results for both bindings. Tools refill their
// bounded window after any child drains; Delegates await their entire batch.
// Admission progress derives from the ordered invocation records, including
// retained settlements, so recovery needs no separate scheduling cursor.
// Each invocation is absent until its start is declared, then the same record
// retains its start receipt and result without a parallel requested marker.
// A rejected Tool child start terminates the Interaction with the original
// Failure, because the ToolSet is required infrastructure. A rejected Delegate
// start is a model-visible rejected result, because choosing another worker is
// the model's delegation policy. A Tool child's Failure propagates unchanged
// to the parent. A drained Delegate subtree with unresolved Effects fails the
// parent without another model call. A canceled parent never resumes to
// collect or publish children.
//
// Each Tool call owns one Effect, its result, and any input continuation.
// Completed siblings retain their settlements when another call remains
// unknown or waits for input. A valid tool.Failure produces its model-visible
// error ToolResult and disposition; its Cause cannot turn it into
// cancellation, input, or host control. Transparent wrappers and joined
// diagnostics preserve that outcome. Ordinary errors, invalid output,
// cancellation, deadlines, and a panic from Tool.Call leave the Tool
// Effect unknown until the Host settles it; terminating the Process retains
// those identities without establishing that external work failed. A panic from
// a tool's input validator is instead a definite failure classified before
// Tool.Call runs: it fails the Step without reaching the model and leaves no
// unknown Effect. Observer callbacks describe
// attempts and never replace the Engine's settlement boundary. A complete
// round continues to the model or completes directly only after its
// Checkpoint is acknowledged. Recovery never replays Tools or models: Hosts
// settle investigated outcomes with [ToolSet.SettleToolResult] or
// [Dispatcher.SettleModelResult], using requests retained by
// [agent.TreeSnapshot.EffectRequest], and submit them through
// agent.Process.ResolveUnknownEffect. Model context receives complete results
// in original call order.
//
// [SettledResults] projects exact known results from the authoritative tree;
// it has no storage or acknowledgment role. [ToolCallRef] correlates one
// logical call across [ToolInvocation.Reference], [RoundResults.Reference],
// and [DelegateAttribution.Reference]; its ProcessID is the requesting
// Interaction, not the Tool child. A Tool child's reference comes only from the
// [ToolChildKey] its parent gave it, so a ToolSet run as a root or under any
// other ChildKey has none.
//
// Descriptor.SignalSchema admits only steering envelopes as unaddressed input.
// [NewSteerSignal] steering accepted during model or child work applies before
// the next model call, after the current result batch; it never preempts a
// model request or answers a Tool input wait. Tool answers address the Tool
// child's wait through [PendingToolInputs]. The answer payload is the original
// response JSON, validated against the unchanged response Schema by that wait;
// schema resources and local references retain their original scope. A parent
// Strategy delivers the same SignalRequest through agent.NewChildSignalEffect.
// Unsupported envelopes are rejected before admission; semantic protocol violations discard the Step
// with a contract Failure, and a Step error never consumes input.
package interaction
