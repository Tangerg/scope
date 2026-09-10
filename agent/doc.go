// Package agent provides the Scope Agent Framework execution kernel.
//
// [Definition] owns immutable behavior and creates serializable [Execution]
// values; [Engine] owns Process lifecycle, [Signal] delivery, [Effect]
// dispatch, child composition, resource bounds, observation, and portable
// snapshots. Strategy payloads stay opaque to the kernel, and persistence
// stays a caller responsibility.
//
// Engine and Process operations require a non-nil context. A nil context is a
// programming error and panics on a valid receiver; use context.TODO when the
// caller has not yet chosen a context. A canceled non-nil context follows each
// operation's cancellation contract.
//
// The kernel exists for one purpose: a multi-step agent with children and
// external side effects must resume after a process restart with a stated
// meaning for every operation that was in flight. Everything below follows
// from that.
//
// # The execution waist
//
// Every strategy intersects on exactly two interfaces:
//
//	type Definition interface {
//		Descriptor() Descriptor
//		Start(Input) (Execution, error)
//		Restore(ExecutionState) (Execution, error)
//	}
//
//	type Execution interface {
//		Step(context.Context, []Signal) (Transition, error)
//		Snapshot() (ExecutionState, error)
//	}
//
// The waist is not generic, because the Engine holds heterogeneous definitions
// homogeneously. [Input], [Output], [Signal], [Effect], and [ExecutionState]
// cross it as bounded, defensively copied JSON. Generics belong to edge
// adapters that convert a Go input to raw input and raw output back to a Go
// output; they never enter the contract the Engine has to hold.
//
// # Step
//
// A Step is one cancellable, discardable, purely candidate reduction. It may
// not call a model, run a tool, perform any other external I/O, hide an
// unbounded loop, or start an unowned goroutine. An external operation can
// only be declared as an [Effect] and executed outside the Step.
//
// These are cooperation contracts for implementations sharing one Go process.
// Capability checks mediate declared Effects; they do not sandbox arbitrary I/O.
// Cancellation and kill still depend on in-flight code returning and external
// operations settling. They cannot forcibly terminate a goroutine.
//
// Three scales explain the kernel: the root tree is the consistency, commit,
// and recovery unit; a Process is the lifecycle and strategy-state isolation
// unit; a Step is the concurrency unit. Adding Processes to a tree adds
// isolated computation and external I/O concurrency, not authoritative commit
// parallelism. Independent commit throughput means separate root trees.
//
// Each root tree has one private commit owner. Pure computation does not
// occupy the commit owner: a Process has at most one Step job in flight and
// siblings run in parallel. Only the owner revalidates and adopts a result.
// Process state owns local admission rules and deterministic transitions;
// the tree owner coordinates cross-Process waits, external jobs, durable
// acknowledgment, and publication. Candidate adoption and wait registration
// succeed together, or all new registrations are rolled back.
// When a kill, pause, cancel, or a new incarnation expires an attempt, the
// result and its error are discarded whole and the Execution is rebuilt from
// committed Execution state.
//
// The owner services continuously ready control requests, job completions, and
// queued Processes in bounded scheduling turns. Queries do not wake execution.
// Pending commits and held freezes still block work that cannot cross those
// boundaries; a checkpoint still requires a safe tree cut. This is a scheduling
// guarantee, not a wall-clock deadline: implementations must honor their bounded
// execution contracts, and the Host owns storage deadlines.
// A progress checkpoint can acknowledge one Process while unrelated sibling
// jobs run. Their cut retains committed state and any prepared Effect frontier;
// their return cannot change that cut until the single commit owner resumes.
//
// Before adopting initial or candidate state, the Engine captures Snapshot and
// successfully restores it through that Deployment's Definition. An
// unrestorable candidate cannot advance signal consumption or dispatch Effects.
// This admission check does not prove exact state equivalence or deterministic
// continuation: the Definition must establish those properties with conformance
// cases. Complete tree recovery also validates runtime identities, mailboxes,
// child ownership, settlements, and the exact Deployment binding.
//
// # Effects and settlement
//
// An Effect is the only way an Execution requests work outside a Step. The
// Engine derives a stable effect identity from the Process identity, step
// sequence, and effect index, then freezes the payload. It interprets only its
// own closed set of framework effects — child and wait operations — and hands a
// strategy effect whole to the dispatcher its [Deployment] bound. A Deployment
// without a dispatcher admits only framework Effects, including during recovery.
// A dispatcher never mutates an Execution; it produces deltas and one settlement
// Signal.
//
// Each effect advances through planned, pending, and settled in declaration
// order, one at a time:
//
//  1. the owner validates candidate state, signal consumption, budget,
//     capability, and batch identity;
//  2. the effect enters pending, and in durable mode the pending boundary
//     commits the whole tree first;
//  3. only then does the dispatcher job start, outside the owner;
//  4. the result is normalized to a definite or an unknown settlement;
//  5. only after the settled boundary succeeds does the owner install the
//     settlement, candidate state, mailbox, and Process transition.
//
// A planned effect that was never dispatched can never become unknown.
// Automatic redelivery is allowed only where replaying one effect identity is
// proven to be the same logical operation; where it is not, an unknown
// settlement stays observable and awaits explicit adjudication. It is never
// silently replayed and never assumed successful. Ephemeral mode runs the same
// state machine without calling the durability port.
// A prepared batch has one execution frontier: definitely settled Effects
// precede at most one pending or unknown Effect, followed only by planned
// Effects. Runtime scheduling and snapshot admission enforce this same order.
// Terminal intent stops the remaining batch without adopting candidate state
// or advancing input consumption. The terminal snapshot retains the started
// prefix's actual settlements and the unstarted planned tail. PreparedEffects
// usage and published child allocations remain charged; unused Step and
// settlement-Signal reservations are released. Restoring this terminal evidence
// neither executes the candidate nor replays its Effects.
// If installing a settled batch fails, the same boundary retains its actual
// settlements without adopting the candidate or partially installing waits.
// A restored pending external attempt becomes Unknown when termination forbids
// replay. An interrupted child start whose child is absent from the authoritative
// cut becomes a failed publication; its Host admission may already have run.
//
// # Signals and waiting
//
// A Signal is the only runtime input into an Execution. [Process.DeliverSignals]
// admits one ordered batch atomically, including a batch with one Signal.
// Repeated submission of one signal identity produces exactly one logical
// consumption and never charges the signal budget twice. The
// same identity with different immutable content is rejected as a conflict.
// In durable mode, successful admission is acknowledged only after mailbox
// records and budget charges commit to the authoritative tree head. The
// consumption cursor advances only when candidate state and transition commit,
// so a failed Step never permanently swallows input.
// ProcessSnapshot.SignalReceipts exposes the same admitted identities and
// committed consumption cursor for delivery reconciliation and input cutover.
// A terminal Process may retain inputs admitted after its final Signal window.
// Their original recipient binding and pending payload remain observable.
// Consumption is bounded by the Signal window delivered to that Step; input
// admitted while the Step runs belongs to a later window.
// Once consumed, a mailbox record keeps its identity, addressed wait, arrival
// order, and normalized payload digest. The payload itself is released with
// candidate adoption. Recovery retains exact pending inputs and validates wait
// history from these facts; consumed content is no longer a transcript.
//
// A wait identity is minted by the Engine; an Execution cannot generate an
// external one. The Execution declares a logical wait through a [Transition];
// the Engine saves the mapping and enqueues an internal Signal carrying the
// identity; on the next Step the Execution records it and enters Waiting
// explicitly. That round trip keeps the Execution the single writer of its own
// state. The Engine wall clock never enters strategy input — business time is
// submitted as an explicit payload.
// Wait registration and its opening Signal are one mailbox operation. Restoring
// history uses the same opening, admission, and consumption rules: an answer
// closes its wait when consumed, and Process termination closes all remaining
// waits. Snapshots whose wait facts contradict that history are rejected.
// Child completions remain queued while their parent is Paused or waiting on
// another WaitID. Only an answer to the current WaitID releases Waiting;
// an explicit pause still requires Resume. Unaddressed Strategy input can also
// queue while Paused or waiting for children without releasing either state.
// [WaitForChildren] requires an explicit [ChildWaitBoundary]. The result boundary
// counts terminal children. The drained boundary counts children whose entire
// subtree satisfies [Process.Join]. All, any, and quorum count those facts in
// request order; none selects successful business outcomes or cancels losers.
// [ChildWaitSatisfied] carries the chosen boundary with the terminal results.
// Wait registration is nonblocking even when a child already reached its boundary.
// [SignalChild] and [CancelChild] declare controls over an exact direct child.
// The tree owner records the recipient change and [ChildControlResult] in one
// durable Effect settlement. Rejected ownership or mailbox admission is a
// definite failed receipt. Signals retain their caller-chosen deduplication
// identity and obey the recipient's safe boundary; cancellation records intent
// and requires a drained wait to establish resource release.
//
// Each strategy declares its own safe consumption boundary and proves it with
// contract tests.
//
// # Process lifecycle
//
// A Process moves through [StatusNotStarted], [StatusRunning], and then one of
// [StatusWaiting], [StatusPaused], [StatusCompleted], [StatusFailed],
// [StatusCanceled], [StatusTimedOut], or [StatusKilled].
//
// A terminal state is decided jointly by the recorded control intent and the
// Step result, never inferred from error text or from context.Canceled alone.
// The matrix is matched in priority order: an explicit kill wins; then a
// reached deadline; then parent or host cancellation; then a contract
// violation, external failure, or panic; then legal completion. A committed
// terminal state is first-terminal-wins, so a late cancellation cannot
// overwrite it. An effect's own cancellation first reaches the strategy as a
// settlement Signal — a local failure is never promoted to a Process terminal
// state on its own.
// [Process.RequestCancellation] also terminates active descendants through
// their owned lifecycle. The surviving parent receives the ordinary completion
// Signal and its Strategy chooses the next transition. Cancellation uses the
// same checkpoint acknowledgment as every other terminal transition.
// Once applied, terminal intent cancels active Step, Dispatch, and child-admission
// contexts throughout the owned subtree without waiting for an ancestor's work
// to return. The owner still collects started external work. Initialization
// outcome and durability acknowledgments are not canceled. A late successful
// child initialization joins the tree under terminal intent before any Step.
// [Process.Await] establishes the Process result and immediate bookkeeping.
// [Process.Join] additionally waits for owned descendant calls and required
// acknowledgments in this runtime. It leaves unrelated siblings running and
// does not release the tree. A runtime failure in the subtree makes Join fail
// after its local calls return, even if this Process already published a result.
// Terminal Unknown settlements remain evidence of remote uncertainty after Join.
// [Engine.Run] composes Start, Join, and Await as one synchronous operation. It
// returns the root result only after the subtree finishes, or a RuntimeError if
// that completion fails. Canceling its context requests termination but does not
// abandon owned work or required acknowledgments.
//
// A child-completion delivery failure is recorded as pending termination.
// Accepted external effects settle first, and any unknown identities remain
// in the terminal result. The pending failure survives tree capture.
//
// A long-lived Engine retains completed trees for diagnostics and capture until
// the Host calls [Engine.ReleaseTree]. Release waits for all descendant work to
// settle, removes the tree from lookup, and leaves existing handles' results
// and runtime errors readable.
//
// Signal identities, wait history, and descendants remain retained for that
// lifetime. Finite budgets and snapshot limits bound one execution; the kernel
// does not prune facts needed for deduplication or extend a tree indefinitely.
//
// [Engine.InspectTree] is the sole live inspection entry. It composes existing
// [ProcessSnapshot] values with current job, commit, and freeze facts, and stays
// available while storage acknowledgment or a tree freeze blocks execution.
// Snapshots own lifecycle, usage, wait authority, and Unknown settlements;
// runtime work may be newer than a durable snapshot. Reports describe one
// owner turn, never drive execution, and add nothing to the recovery schema.
// Synchronous [EventListener] callbacks must not query, control, or Await their
// tree. Calls that forward the callback context receive [ErrListenerReentrancy]
// while that invocation is active. Different tree owners remain independently
// callable, but callbacks must avoid cyclic waits and return in bounded time.
// Engine.Close requires every tree's publication, bookkeeping, and owned work
// to finish in both durable and ephemeral mode. Join or Run establishes this
// subtree completion; any separately acquired freeze must also be released.
// Close(ctx) closes admission once and joins Engine-owned observation shutdown;
// canceling ctx ends only that caller's wait. DeltaListener callbacks must not
// call Close or FlushDeltas on their Engine because both join Delta delivery.
// Forwarding an active callback context makes those calls fail with
// ErrListenerReentrancy.
//
// A durable writer can stop without terminating the logical execution. Storage
// failures and ownership conflicts reach [Process.Await] as a [RuntimeError]
// with no Result. The error preserves the original cause, the last acknowledged
// head, and uncertain Effect identities. [EventRuntimeStopped] describes this
// instance failure; it never substitutes for [EventProcessFinished]. Status and
// usage in durable mode project only acknowledged tree state. The Host reads
// its authoritative head before reactivation, since a lost commit response or
// another writer may have advanced it beyond the stopped instance's view.
//
// # Recovery
//
// [ExecutionState] is a discriminated envelope of a kind and an opaque
// payload. The kernel constrains the envelope and never interprets the payload
// recursively; each strategy owns and guards its own wire shape. A host may
// persist the envelope but must not parse it by kind and join strategy control
// flow. Recovery finds the Definition through an exact [DeploymentRef]; a
// global kind-to-factory switch is forbidden.
//
// [TreeSnapshot] is the canonical recovery state of a complete root tree.
// It uses one current strict wire shape without a version envelope or migration
// dispatch; parsing validates the structure and the recorded domain facts.
// [ProcessSnapshot] is a single-Process diagnostic value and is not a recovery
// unit. Events and [Delta] values record attempts and observations only; they
// never substitute for an acknowledged TreeSnapshot.
// Committed events wait for durable acknowledgment. Event sequences describe
// publication within one runtime activation and restart when a nonterminal
// Process is restored; they do not change snapshot contents or trigger commits.
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
// its goap subpackage supplies a bounded search implementation selected by the
// Host. [github.com/Tangerg/scope/agent/strategy/workflow] implements ordered
// deterministic stages over a closed vocabulary, composing real child Processes.
// [github.com/Tangerg/scope/agent/strategy/coordination] composes bounded input
// gates, absolute deadlines, and first-success competition through the same
// child and wait contracts.
// [github.com/Tangerg/scope/agent/strategy/collaboration] runs bounded coordinator
// turns beside background workers, composes task controls, and waits for drained
// results before continuing decisions over explicit working state.
//
// The messaging package delivers intermediate input through a narrow Host
// port, retaining the original recipient and Effect-derived Signal identity.
//
// The Engine never imports or type-switches a concrete strategy. A new
// strategy is admitted by implementing the waist, state codec, and safe
// consumption boundary, and binding a dispatcher when it declares external Effects.
//
// # Boundaries
//
// A long-lived Host can run successive bounded root trees. The successive
// episodes example under Engine.Start establishes a completed, joined predecessor,
// seals input routing, reconciles SignalReceipts, and binds explicit successor
// state, Deployment, limits, and authority. Its Host transaction links the
// successor identity with the initial tree checkpoint; a lost start response is
// reconciled by restoring that identity. Calling Start with equal Input alone
// does not provide idempotent successor admission. Retained unconsumed inputs
// keep their predecessor address, and unresolved descendant Effects prevent the
// example's safe boundary. Production Hosts implement the transaction and
// retention policy in their own durable storage.
//
// The framework owns definition validation, deployment freezing, the Process
// state machine, signal ordering and deduplication, effect identity and
// settlement, budgets, the lifecycle, framework events, and the snapshot and
// recovery protocol.
//
// The host owns product identity, transports, stores and transactions,
// permissions and billing, deployment catalogs and routing, provider and model
// selection, storage acknowledgment, and retention of its own facts. A host
// depends only on this neutral lifecycle contract and never parses a strategy's
// snapshot payload.
//
// Production database adapters and their storage-specific integration tests
// belong to the consuming application or an independently owned adapter. The
// agenttest package supplies shared durability and Definition conformance suites.
//
// Chat, tools, embeddings, history, and telemetry stay in their own modules.
// Agent reuses them and duplicates none of them.
package agent
