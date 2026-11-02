// Package interaction composes chat models, tools, and delegates into managed
// Programs with [Input] and [Output]. [Compile] requires the Runtime's immutable
// Compiler, an implementation Revision, and explicit configuration. The Compiler
// freezes configuration and exact child bindings into the Program identity.
//
// Each Execution owns its conversation and decision progress. Its definition
// retains immutable tool declarations, validators, and classifiers; only the
// dispatcher holds executable models and tools. Every model request, including
// its advertised tools, is frozen in an Effect before external work starts.
// [ModelRequestFromEffect] and [ToolCallFromEffect] decode committed intent;
// [ModelSettlementFromOutcome] and [ToolSettlementFromOutcome] decode its outcome.
// Transient streaming deltas do not establish durable results.
//
// Ordinary tool calls are direct dispatcher Effects. Their first EffectID is
// the logical reference across continuation Effects and result projections, and
// every attempt receives it as tool.Admission.IdempotencyKey so external
// systems can deduplicate replayed work. [ToolInvocation.IdempotencyKey] gives
// Hosts the same key when they reconcile an Unknown call.
// A tool.InputRequest opens a local Wait; each later question receives a new
// WaitKey so an old answer retry cannot answer a new question. [PendingToolInputs]
// lists those waits from a run.TreeInspection, their only record. The process and WaitKey own
// answer routing; tool call names and model call IDs do not.
//
// Delegate calls use Spawn and consume the child's Exit. An Exit establishes
// that the entire subtree drained, including uncertain external work. Delegates
// and tools retain results in model proposal order even when they complete out
// of order. Tool concurrency and resource classification bound admission;
// completed siblings need not wait for unrelated tools before freeing a slot.
// A whole proposal is validated before any of its calls execute.
//
// A definite tool.Failure becomes model-visible feedback. Ordinary errors,
// invalid external results, and panics remain Unknown because they cannot prove
// that external work did not happen. Hosts investigate and submit a settlement
// through run.Handle.ResolveUnknownEffect. [SettleToolResult], [SettleModelResult],
// and [SettlePreparedContext] preserve the interaction's typed result contract.
// Tool recovery accepts the complete tool.Result, including an input request
// whose external receipt was lost before its question reached the Log.
// An interrupted call is redispatched only when its Tool declares
// [IdempotentTool]; every other interrupted call settles Unknown.
// Runtime failures are recorded separately from opaque dispatcher outcomes.
//
// [NewSteerSignal] accepts unaddressed steering at a safe conversation boundary.
// Steering received during model or tool work applies before the next model
// request; it neither cancels that work nor answers a tool's addressed Wait.
// Completion validators and context policies execute in discardable Steps and
// must be deterministic, bounded, side-effect-free, and cancellation-aware.
// External context processing belongs in its dispatcher Effect instead.
//
// [SettledResults] and [DelegateAttributions] project the complete history that
// run.Runtime.History reads, because a checkpoint does not keep settled rounds.
// They own no acknowledgment, execution, persistence, or retry policy.
package interaction
