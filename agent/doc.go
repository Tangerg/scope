// Package agent provides the Scope Agent Framework execution kernel.
// [Definition] owns immutable behavior and creates serializable [Execution]
// values. [Engine] owns Process lifecycle, Signal delivery, Effect dispatch,
// child composition, resource bounds, observation, and tree recovery.
// Strategy payloads remain opaque to the kernel.
//
// # Execution and ownership
//
// A root tree is the consistency, commit, and recovery unit. A Process isolates
// lifecycle and Strategy state; a Step is the concurrency unit. Sibling Steps
// may run concurrently, but one tree owner revalidates and adopts their results.
// Independent commit throughput requires separate root trees.
//
// Execution.Step is cancellable, discardable candidate computation. It must
// not perform external I/O, hide unbounded work, or start unowned goroutines.
// External work is declared as an [Effect] and performed by a [Dispatcher].
// These are cooperation contracts, not a sandbox: cancellation cannot forcibly
// terminate a goroutine, and implementations must return in bounded time.
// Expired Step attempts are discarded, including their errors; Execution is
// rebuilt from the last adopted state. Before adoption, the Engine validates
// that the Deployment can restore the candidate's Snapshot. Definitions own
// state equivalence and deterministic continuation, verified by conformance cases.
//
// [Continue] permits further private candidate computation. [Checkpoint]
// requires acknowledgment before another Step or Effect in that Process runs.
// Pending commits and freezes block work that cannot cross their boundary;
// inspection never drives execution. The owner services ready work in bounded
// scheduling turns, while the Host owns storage deadlines.
//
// # One commit protocol
//
// Every Engine requires an explicit [TreeCommitter]. [NewMemoryTreeCommitter]
// supplies the same compare-and-swap, writer fencing, and deduplication protocol
// with retention limited to that store instance's lifetime. Persistent adapters
// provide their own storage transactions. No backend bypasses acknowledgment.
//
// Start commits the initial head and writer identity before publishing the
// Process. Every [TreeSnapshot] carries that identity. RestoreTree validates the
// exact Deployment binding and atomically activates a new writer against the
// supplied head; missing heads, stale digests, and superseded writers fail.
// Recovery retains captured authority, limits, budgets, and usage. The Host
// authorizes that captured authority under current policy before restoration.
//
// The runtime may hold a newer private candidate than its acknowledged head.
// Signal admission, child publication, committed Events, terminal results, and
// inspection snapshots become visible only after their tree commit succeeds.
// [Engine.CaptureTree] freezes a safe cut and acknowledges it before returning;
// it cannot manufacture recovery state after a runtime failure.
// [Engine.InspectTree] combines acknowledged Process snapshots with current job,
// commit, and freeze facts. Those work facts may be newer than the snapshots.
//
// A failed acknowledgment stops the writer with a [RuntimeError], without
// inventing a logical termination or overwriting an already published result.
// The error carries its last acknowledged digest and uncertain Effect identities.
// Recovery must load the store's authoritative head: a lost response or another
// writer may have advanced storage beyond this runtime's knowledge.
//
// # External Effects and uncertainty
//
// Dispatcher Effects advance in declaration order through planned, pending,
// and settled. The pending boundary commits the complete prospective tree before
// dispatch starts. Settlement commits before continuation or publication. A
// returned error means the external outcome is unknown; it is not definite
// failure. A planned Effect that never dispatched cannot become unknown.
// Direct-child controls perform no external I/O: recipient changes and their
// definite settlement share one atomic tree checkpoint.
//
// An interrupted pending attempt may replay only when its [ReplayPolicy]
// establishes the same logical operation under the original identity. A settled
// Unknown requires explicit Host reconciliation or [Process.ReplayUnknownEffect].
// The existing Unknown remains authoritative throughout replay until a definite
// resolution commits. Cancellation still collects already started external work.
// Terminal snapshots retain actual settlements, unstarted tails, and unresolved
// identities; restoration never silently turns uncertainty into success.
//
// # Signals, lifecycle, and bounds
//
// [Signal] is the only runtime input into Execution. [Process.DeliverSignals]
// atomically admits an ordered batch and charges each identity once. Reusing an
// identity with different immutable content conflicts. [SignalReceipt] exposes
// acknowledged admission and consumption for delivery reconciliation. Waits and
// child waits carry explicit identities; stale continuation input is rejected.
// Candidate failure cannot permanently consume an input.
//
// Terminal intent cancels owned Step, Dispatch, and child-admission contexts;
// it still joins started work and required acknowledgments. A late successful
// child initialization joins under that intent before running a Step.
// [Process.Await] returns an acknowledged result; [Process.Join] additionally
// waits for descendant work and bookkeeping. [Engine.Run] combines Start, Join,
// and Await. Terminal Unknown settlements remain evidence of remote uncertainty.
//
// [Engine.ReleaseTree] removes a drained tree from Engine lookup without
// deleting storage or invalidating existing handles. [Engine.Close] requires
// publication, bookkeeping, owned work, and separately acquired freezes to finish.
// Host cancellation does not abandon required acknowledgments. Engine and Process
// operations require non-nil contexts; nil is a programming error.
//
// [Limits] separates cumulative Budget authority from mailbox and snapshot
// capacity. Zero Quota is unlimited, including during recovery. Finite parents
// permanently charge child grants; unlimited grants do not debit a finite
// counter. Captured quotas survive restoration without new Engine defaults.
// Unlimited execution still retains history and checks numeric identity overflow;
// the Host chooses retention and resource policy.
//
// Snapshot admission reserves bounded control and termination metadata with
// worst-case JSON escaping. Admission of one cut does not authorize future
// growth. An external result that cannot fit stops the runtime with its unresolved
// identity; the Host reconciles the outcome from the acknowledged stored head.
//
// # Recovery and observation
//
// [TreeSnapshot] is the sole recovery representation, with one strict current
// schema and no version envelopes, migration dispatch, or compatibility reads.
// [ProcessSnapshot] is diagnostic state, not an independent recovery unit.
// [ExecutionState] contains opaque Strategy-owned state; Hosts resolve an exact
// [DeploymentRef] rather than interpreting Strategy kinds or control flow.
//
// Events describe attempts or committed facts; [Delta] is best-effort observation.
// Neither substitutes for an acknowledged head. Event sequences restart with
// each activation and do not impose ordering across writers. Wall timestamps
// are diagnostic, not causal proofs. Listener callbacks must return in bounded
// time and avoid reentrant control or cyclic waits; see [EventListener],
// [DeltaListener], and [ErrListenerReentrancy].
//
// # Strategies
//
// Built-in strategies live under strategy/. Each concrete package implements
// the public execution protocol; the directory itself defines no package or
// runtime contract. Host-defined strategies use the same protocol.
//
// [github.com/Tangerg/scope/agent/strategy/interaction] implements ReAct-style
// model and tool loops with working context, delegates, and artifacts.
// [github.com/Tangerg/scope/agent/strategy/planning] owns goal-driven planning;
// its goap subpackage supplies a quota-controlled search implementation selected by the
// Host. [github.com/Tangerg/scope/agent/strategy/workflow] implements ordered
// deterministic stages over a closed vocabulary, composing real child Processes.
// [github.com/Tangerg/scope/agent/strategy/coordination] composes bounded input
// gates, absolute deadlines, and first-success competition through the same
// child and wait contracts.
// [github.com/Tangerg/scope/agent/strategy/collaboration] runs coordinator
// turns beside background workers. Decisions choose whether to continue while
// workers run, wait for drained results, or complete. The Strategy retains
// explicit working state and immutable child bindings; the Engine resolves and
// runs the children. Workflow child failures and collaboration coordinator
// failures preserve their original Failure kind, code, and diagnostic.
//
// [github.com/Tangerg/scope/agent/messaging] delivers intermediate input through
// a narrow Host-authorized port, retaining the original recipient and
// Effect-derived Signal identity. Its sender and recipient acknowledge
// separately; it does not provide the direct-child control's single tree commit.
//
// The Engine never imports or type-switches a concrete strategy. A new
// strategy is admitted by implementing the waist, state codec, and safe
// consumption boundary, and binding a dispatcher when it declares external Effects.
//
// # Host boundaries
//
// Hosts own product identity, transports, storage transactions, retention,
// permissions, billing, deployment catalogs, and provider selection. The kernel
// owns execution identities, state transitions, resource accounting, child
// ownership, and the commit protocol. Successive bounded root trees require a
// Host transaction for successor admission and input cutover; equal Start input
// alone does not make admission idempotent.
//
// Database adapters belong to the consuming application or an independently
// owned adapter. Package agenttest supplies Definition and TreeCommitter
// conformance suites. Chat, tools, embeddings, history, and telemetry remain in
// their own modules.
package agent
