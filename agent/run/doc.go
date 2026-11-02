// Package run provides the Scope Agent Framework execution kernel.
// [Definition] owns immutable behavior and creates serializable [Execution]
// values. [Runtime] owns Process lifecycle, Signal delivery, Effect dispatch,
// child composition, resource bounds, observation, and tree recovery.
// Strategy payloads remain opaque to the kernel. Inside Execution.Step, a
// strategy reads [Signal.Frame] with the expected concrete type, or selects
// [SignalFrame] for a type switch over a mixed inbox. [ExternalInput] and
// [Settlement] keep sender data separate from typed child and wait receipts.
//
// # Running a typed program
//
// A [Program] binds a Definition, its Dispatcher, and child programs to typed
// input and output and an exact [ProgramRef]. Compile an agent with
// [github.com/Tangerg/scope/agent.Compile], or use [Bind] for a custom Definition. Create a [Runtime] with
// [New] and an explicit [Store].
//
// [Runtime.Run] runs a Program[I, O] in the foreground, using the caller's
// context for execution. It joins local work in the tree and releases its local
// registration before returning O or an error. A logical failure is available
// as a [TerminationError]. Joining cannot forcibly stop an external operation.
//
// [Runtime.Start] accepts a [StartRequest] with typed input and a stable,
// Host-authorized [StartID] known before submission. An accepted managed run
// outlives the request context. Its first commit binds that ID, the exact program
// and input intent, and the root process ID. Repeating the same intent reuses
// the acceptance; different intent returns [StartConflictError]. A [StartError]
// means the initial acknowledgment is uncertain. Reconcile with the same ID.
// A retry returns a local Handle only when [HeadReader] confirms the same
// writer incarnation. This check does not pin ownership against a later takeover.
// Otherwise [StartAcceptedError] carries the receipt, even when the store
// lacks a [HeadReader]. A head read error is joined with that evidence.
// [Runtime.StartedTrees] reads a supported store without acquiring the writer.
//
// A managed [Handle] exposes [Handle.Answer] for an addressed wait, [Handle.Await]
// for the committed logical result, and [Handle.Join] for local subtree drainage.
// Canceling a waiter's context does not cancel the managed run. Use [Handle.Cancel]
// to submit cancellation and [Runtime.ReleaseTree] to release its local registration.
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
// rebuilt from the last adopted state. Before adoption, the Runtime validates
// that the bound Definition can restore the candidate's Snapshot. Definitions own
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
// Every Runtime requires a [Store] for atomic tree commits and start receipts. [NewMemoryStore]
// supplies the same compare-and-swap, writer fencing, and deduplication protocol
// with retention limited to that store instance's lifetime. Persistent adapters
// provide their own storage transactions. No backend bypasses acknowledgment.
// Checkpoints and Effects share an incarnation-scoped commit sequence, allocated
// by the Runtime and atomically retained with the stored head. Equal snapshot
// content can recur; it never identifies a commit or permits historical replay.
// [EffectBoundary], [TreeCheckpoint], and [TreeActivation] derive the canonical
// commit Identity and ContentDigest used by both memory and persistent stores.
// A Host stores these Scope facts alongside its own business write set, within
// the same atomic head comparison. The [TreeCommitter] example also binds the
// real storage call to a Host deadline and shutdown signal.
//
// Start commits the initial head and writer identity before publishing the
// Process, and every [TreeSnapshot] carries that identity. [Runtime.Recover]
// activates a new writer against the supplied head; missing heads, stale
// digests, and superseded writers fail.
//
// The runtime may hold a newer private candidate than its acknowledged head.
// Signal admission, child publication, committed Events, terminal results, and
// inspection snapshots become visible only after their tree commit succeeds.
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
// A Dispatcher's [EffectPolicy] owns each of its Effects' required
// capabilities; the Runtime enforces them when preparing a Step, restoring a
// dispatchable Effect, and replaying an Unknown.
//
// An interrupted pending attempt may replay only when its [ReplayPolicy]
// establishes the same logical operation under the original identity. A settled
// Unknown requires explicit Host reconciliation or [Handle.ReplayUnknownEffect].
// The existing Unknown remains authoritative throughout replay until a definite
// resolution commits. Cancellation still collects already started external work.
// Terminal snapshots retain actual settlements, unstarted tails, and unresolved
// identities; restoration never silently turns uncertainty into success.
//
// # Signals, lifecycle, and bounds
//
// [Signal] is the only runtime input into Execution. [Handle.DeliverSignals]
// atomically admits an ordered batch and charges each identity once. Reusing an
// identity with different immutable content conflicts. [SignalReceipt] exposes
// acknowledged admission and consumption for delivery reconciliation. Waits and
// child waits carry explicit identities; stale continuation input is rejected.
// Candidate failure cannot permanently consume an input.
//
// Terminal intent still joins started work and required acknowledgments; a late
// successful child initialization joins under that intent before running a
// Step. Host cancellation never abandons a required acknowledgment. Terminal
// Unknown settlements remain evidence of remote uncertainty.
//
// Each Process carries its own cumulative [Budget]; one [TreeLimits] per root
// tree owns mailbox, snapshot, and structural capacity for every member.
// Captured quotas survive restoration without new Runtime defaults.
// Unlimited execution still retains history and checks numeric identity overflow;
// the Host chooses retention and resource policy. Default depth and active-child
// bounds do not bound retained tree size or lifetime work. Hosts requiring bounded
// lifetime growth must choose finite Steps, Effects, Signals, Process snapshot bytes, total
// children, tree Processes, and tree snapshot bytes for their workload. See the
// bounded configuration in the [Definition] example. These quotas do not cap the
// committer ledger across trees or repeated control checkpoints; storage retention
// has its own Host-owned lifetime.
//
// Defaults suit trees whose input sizes, lifetime work, and population are
// bounded by the Host. They are not a memory budget or a latency guarantee.
// A pending-Signal bound does not limit consumed deduplication history. Releasing
// a Runtime tree does not remove the committer's retained head or replay facts.
//
// Snapshot admission reserves bounded control and termination metadata with
// worst-case JSON escaping. Admission of one cut does not authorize future
// growth. An external result that cannot fit stops the runtime with its unresolved
// identity; the Host reconciles the outcome from the acknowledged stored head.
//
// # Capacity measurements
//
// From the agent module root, run the kernel benchmarks for a local baseline:
//
//	go test ./run -run '^$' -bench '^BenchmarkTreeSignalCommit$/[^/]+/batch_(1|64)$/' -benchmem -benchtime=1000x -count=3
//	go test ./run -run '^$' -bench '^BenchmarkTreeRecoveryBoundary$/(baseline|history_1024|active_batch_64|combined)$/(restore|parse_restore)$' -benchmem -benchtime=1000x -count=3
//	go test ./run -run '^$' -bench 'BenchmarkWaitingTreeCapture|BenchmarkMemoryStoreRetention|BenchmarkTreeCaptureGrowthReduction' -benchmem -benchtime=10x -count=3
//
// Signal commit samples run from DeliverSignals through checkpoint acknowledgment
// on a fresh copy of the same paused tree. The default-width fixture retains
// a root and DefaultTreeLimits().MaxActiveChildren completed children; expanded
// fixtures explicitly raise limits. The mailbox-capacity fixtures distinguish
// pending payloads from consumed identities. Prepared Effect counts include
// requests not yet dispatched; unknown_effects counts actual uncertain outcomes.
// TreeRecoveryBoundary/restore starts from a decoded snapshot; parse_restore
// includes strict parsing. Both stop when Runtime.Recover returns, including volatile
// writer activation, before subsequent execution or remote reconciliation.
//
// p50/p95/p99 use nearest-rank serial, closed-loop samples. Read their sample
// counts; the check runner's one-iteration smoke is not a tail-latency baseline.
// Sample the mailbox-capacity and large-context cases separately at a fixed
// count suited to their higher cost; report that count with their results.
// Record the revision, Go version, CPU, concurrency, and repeated results on the
// same machine. These fixtures exclude Host storage reads, database/network I/O,
// model/tool latency, exporter I/O, and sustained competing arrivals. The 1 ms
// acknowledgment delay models waiting only. Measure those Host costs under the
// intended arrival rate before choosing quotas or a latency objective.
//
// MemoryStoreRetention reports the latest head's wire size, retained
// replay key/digest payload bytes, and cumulative offered snapshot bytes. The
// byte counts omit the additional decoded representation and Go object/map
// overhead; B/op measures allocation churn, not retained heap.
// TreeCaptureGrowthReduction measures full
// snapshot traffic as opaque state grows and shrinks. Large synthetic tree and
// context fixtures characterize scaling; they do not certify defaults for every
// combination or authorize a different persistence or writer protocol.
//
// # Recovery and observation
//
// [TreeSnapshot] is the sole recovery representation, with one strict current
// schema and no version envelopes, migration dispatch, or compatibility reads.
// [ProcessSnapshot] is diagnostic state, not an independent recovery unit.
// [ExecutionState] contains opaque Strategy-owned state; Hosts resolve an exact
// [ProgramRef] rather than interpreting Strategy kinds or control flow.
//
// Events describe attempts or committed facts; [Delta] is best-effort observation.
// Neither substitutes for an acknowledged head. Event sequences restart with
// each activation and do not impose ordering across writers. Wall timestamps
// are diagnostic, not causal proofs. [EventListener] and [DeltaListener] own
// their callback bounds and reentrancy rules. Each registration has an independent
// bounded lane; use [Runtime.FlushObservations] when reading side effects from
// callbacks after a run. [Handle.Observe] is the snapshot and cursor interface
// when missed updates require explicit resynchronization.
//
// # Strategies
//
// The agent package and its strategy subpackages compile behavior for the same
// public execution protocol. Host-defined strategies use the same protocol.
//
// [github.com/Tangerg/scope/agent] implements ReAct-style
// model and tool loops with working context, delegates, and artifacts.
// [github.com/Tangerg/scope/agent/planning] owns goal-driven planning;
// its goap subpackage supplies a quota-controlled search implementation selected by the
// Host. [github.com/Tangerg/scope/agent/workflow] implements ordered
// deterministic stages over a closed vocabulary, composing real child Processes.
// [github.com/Tangerg/scope/agent/coordination] composes bounded input
// gates, absolute deadlines, and first-success competition through the same
// child and wait contracts.
// [github.com/Tangerg/scope/agent/team] runs coordinator
// turns beside background workers. Decisions choose whether to continue while
// workers run, wait for drained results, or complete. The Strategy retains
// explicit working state and its Program binds the coordinator and workers;
// the Runtime starts the children from those bindings. Workflow child failures and collaboration coordinator
// failures preserve their original Failure kind, code, and diagnostic.
//
// [github.com/Tangerg/scope/agent/messaging] delivers intermediate input through
// a narrow Host-authorized port, retaining the original recipient and
// Effect-derived Signal identity. Its sender and recipient acknowledge
// separately; it does not provide the direct-child control's single tree commit.
//
// The Runtime never imports or type-switches a concrete strategy. A new
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
package run
