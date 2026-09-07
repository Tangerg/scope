# Kernel and strategy boundary

Status: design analysis of strategies as software running on a shared execution
kernel, the boundaries this model requires, and the remaining improvements.

This document uses the operating-system analogy to evaluate ownership,
composition, and extension. The authoritative contracts live in
[GoDoc](../doc.go) and checked examples. It does not restate durability
direction: that lives in
[`durable-runtime-roadmap.md`](durable-runtime-roadmap.md). Remove a direction
here once it lands or is rejected, rather than keeping a second description of
the runtime.

## The lens

The kernel is small, knows nothing about the behavior it runs, and mediates
every declared Effect. A conforming strategy requests external work through
that boundary. The Go interfaces establish a cooperation contract; they do not
sandbox an implementation that performs undeclared I/O or ignores cancellation.
Reading the boundary as an operating-system boundary is useful because it makes
the right questions obvious: what is the trap gate, what is the call table, who
holds privilege, who accounts for resources, what identifies a program image,
and what the kernel does when a program misbehaves.

The lens is a lens, not a target. Two places where it must not be followed:
the kernel schedules cooperatively on purpose, and it has no ambition to become
a general application platform. Where the analogy suggests a feature, the
feature still needs a demonstrated framework-level need.

The useful design objective is that an author can supply a new strategy as a
complete behavior, bind its external dependencies, and run it under the same
execution rules without changing the kernel. Interaction, planning, and workflow
are implementations of that model, not an exhaustive taxonomy for the Engine
to recognize. The kernel's size is judged by the responsibilities it owns, not
by a target line count or the number of features familiar from an OS.

## Strategies as software

### Map responsibilities before mapping names

| OS concept | Agent counterpart | What the analogy establishes |
| --- | --- | --- |
| Program definition | `Definition` and its `Descriptor` | Behavior has an explicit contract independent of a running instance. |
| Bound program image | `Deployment`, identified by `DeploymentRef` | Definition, Dispatcher, and frozen behavior configuration are selected together. |
| Process | `Process` containing a strategy-owned `Execution` | Runtime lifecycle and private strategy state have distinct owners. |
| System call | An `Effect` declared by a Step | An intent crosses an owned boundary before external work executes. |
| Driver | The Deployment's `Dispatcher` | External work has implementation-specific execution and replay semantics. |
| Event input | `Signal` | Results and accepted external inputs enter an ordered consumption boundary. |
| Permissions and quotas | `CapabilitySet`, `Limits`, `Budget`, and `TreeLimits` | Declared work is subject to authority and resource accounting. |
| Loader | `DeploymentResolver` | Recovery and child startup resolve an exact behavior binding. |

These are explanatory correspondences, not proposed public types. In
particular, there is no need for a second `Program`, `Driver`, or `Syscall` API
alongside the existing domain owners. A Go module is a distribution and
dependency boundary; a Deployment is a runtime behavior binding. Their
cardinalities need not match.

The root tree also has a responsibility that this table deliberately leaves
without an OS counterpart: it owns consistency, commit, and recovery across its
Processes. An incarnation identifies an active writer of that tree. Treating
either as another spelling of Process would erase a boundary the durability
protocol needs.

### A software identity includes its external interpretation

[`Deployment`](../deployment.go) is the closest existing counterpart to bound
software because it pairs the Definition with the Dispatcher that understands
its Effects. A Definition alone does not identify all behavior: a different
dispatcher policy or model configuration can change the outcome of the same
declared request. The implementation and configuration digests must cover the
artifacts and frozen settings required by the deployment contract. A digest is
an identity assertion, not proof that an author included every dependency or
that a remote service will produce identical future results.

The Descriptor describes the portable strategy contract. The DeploymentRef
identifies its exact binding. A Process holds one running lifecycle, and its
Execution owns the state needed to continue. Keeping these concepts separate
allows many isolated executions of one definition without mutable process state
leaking into a shared definition or deployment catalog.

Recovery selects the recorded DeploymentRef. Selecting a new deployment for new
work is a Host decision; resolving an old reference to the latest implementation
would silently change an existing execution. The software analogy does not
establish binary compatibility, automatic upgrades, or state migration. Scope
retains one strict current schema and replaces obsolete APIs outright during
development. The Host must make artifact availability and unsupported recovery
explicit instead of relying on name-based fallback.

### A strategy is a program with explicit suspension points

The authoring model is a state machine that exposes its continuation. Pure
decision logic lives in Execution; external operations are declared as Effects;
results return through Signals. Waiting is an explicit lifecycle state, and
Snapshot contains the strategy state needed for deterministic continuation.
An author does not retain a goroutine stack as the authoritative continuation.

This makes an important part of agent software reusable: a planning algorithm
can choose actions, an interaction loop can choose model and tool requests, and
a workflow can choose child execution order under one lifecycle contract.
Their domain payloads and decision procedures remain different. A common
execution contract does not make arbitrary strategy inputs, outputs, or Effect
payloads interchangeable; a composition must validate the contracts it joins.

The practical benefit is that a strategy author can reuse scheduling, input
delivery, effect settlement, resource accounting, and recovery coordination.
The corresponding obligation is to supply complete private state, a safe Signal
consumption boundary, bounded execution, and correct external replay semantics.
These obligations belong in GoDoc and checked examples, so software authors can
prove conformance without learning the private scheduler.

## Separate execution rules, decisions, and integration policy

The OS lens is most useful when it locates a decision. The following ownership
split applies the repository's [design philosophy](../../DESIGN_PHILOSOPHY.md)
to Agent; it does not make the Agent kernel the owner of all Scope capabilities.

| Owner | Responsibility | Boundary to preserve |
| --- | --- | --- |
| Kernel | Process lifecycle, Signal admission and consumption, Effect identity and settlement, authority checks, resource accounting, tree commit coordination | It never selects a planning algorithm, interprets private strategy state, or routes by strategy name. |
| Strategy | Decision procedure, private state invariants, domain payload validation, choice of Effects, and response to results | It uses the shared lifecycle instead of implementing another scheduler, mailbox, or recovery protocol. |
| Dispatcher and external adapters | Interpret that strategy's external requests, bind existing clients, and establish conclusive or unknown outcomes and replay guarantees | They do not mutate Execution or bypass Kernel settlement by delivering a private result directly. |
| Host | Bind deployments and adapters, select resource and admission policies, supply storage transactions, route new work, and manage runtime activation and release | It consumes the neutral runtime contract without parsing strategy snapshots to drive behavior. |
| Flame | Product sessions, user-facing approvals, desktop workflows, deployment catalogs, billing, and marketplaces | Product lifecycle and presentation do not become mandatory framework concepts. |

The Host is a composition role that any consuming application can fill; running
Scope does not require Flame. The Kernel enforces the selected permissions,
while the Host decides which permissions to grant. The Kernel defines the
acknowledgment protocol, while the Host's durability implementation supplies the
transaction that fulfills it. Separating policy from mechanism therefore still
requires a clear enforcement owner.

Direct use needs only a bound Deployment and an Engine. Applications add an
exact DeploymentResolver for cross-deployment children or recovery, and a
TreeDurability adapter when persistence is required. Deployment catalogs, active
version selection, and routing are optional Host governance. Their absence does
not require an application to rebuild runtime execution or adopt a platform
container before it can run a strategy.

Core chat, tools, retrieval, and other capabilities remain directly reusable
through their own canonical APIs. They do not need to become Processes merely
because Agent can compose them. A Dispatcher that calls a model reuses the Core
client contract; introducing another model client inside the Kernel would make
the OS analogy expand Scope's abstractions instead of clarifying them.

### Keep the Effect boundary common and its domain interpretation local

Every Effect participates in the same runtime identity, validation, and
settlement rules. The Kernel interprets its closed framework operations for
children and waits; a Deployment's Dispatcher interprets strategy-owned Effects.
This split allows new external behavior without adding a central table of model
vendors, tool types, or strategy-specific commands to the Kernel.

An opaque payload remains subject to domain validation by its owner. The Kernel
validates the envelope and common execution rules, and the strategy or Dispatcher
validates the meaning it consumes. A new domain operation cannot claim new
authority by choosing a payload name. Adding another wrapper API for the same
atomic operation would also create a second contract without a second owner.

Kernel extensions need evidence that an existing primitive cannot express the
required lifecycle correctly. Frequency of use across strategies is not enough:
several strategies calling models can share the existing model contracts. A
durable timer is a different kind of candidate because future delivery across
restart has identity and lifecycle requirements that a sleeping Dispatch cannot
fulfill by itself. That distinction justifies investigating the lifecycle; it
does not settle the timer's placement or design in advance.

## What the boundary already provides

Every replaceable seam is a narrow interface owned by the consuming side:

| Seam | Contract | Role |
| --- | --- | --- |
| Behavior | `Definition` + `Execution` | Immutable behavior and its private running state |
| External work | `Dispatcher` | The driver that performs one frozen intent |
| Behavior lookup | `DeploymentResolver` | Exact-match loader, no routing, no fallback |
| Persistence | `TreeDurability` | The journal the kernel acknowledges against |
| Admission | `ProcessAdmitter` | The policy gate before a Process exists |
| Observation | `EventListener`, `DeltaListener` | Read-only, control-free taps |

No interface listed here exceeds three methods. The kernel imports no strategy
package and type-switches no concrete strategy, so replacement depends on the
shared contracts rather than knowledge of a particular implementation.

Four mechanisms validate and contain work submitted through the boundary:

**One trap gate per seam.** Every crossing into strategy or host code goes
through a single wrapped call: [`execution_boundary.go`](../execution_boundary.go)
for the behavior contract, [`effect_dispatch.go`](../effect_dispatch.go) for
dispatch, [`child_start.go`](../child_start.go) for deployment resolution,
[`tree_durability.go`](../tree_durability.go) for persistence,
[`process_admission.go`](../process_admission.go) and
[`process_start_outcome.go`](../process_start_outcome.go) for admission, and
[`observation.go`](../observation.go) for listeners. These gates recover panics
in the calls they wrap and preserve each boundary's error semantics: behavior
panics become Process failures, dispatch panics leave an unknown settlement,
durability errors stop the durable writer with a `RuntimeError`, and listener
panics are counted without changing execution. Adding an outward call that
bypasses its gate would bypass that boundary's failure handling.

**Monotone privilege.** A `CapabilitySet` is checked when an Effect is
prepared, checked again when a child requests authority, and re-validated when
a snapshot is parsed. A child can never hold authority its parent lacks, and
recovery cannot reintroduce an escalation that runtime rejected.

**Hierarchical accounting.** `Limits` bounds one Process, `Budget` is
permanently transferred from parent to child, and `TreeLimits` bounds the
shape of the tree. Every quantity is countable and charged before the work
happens, so a bound cannot be exceeded and then discovered.

**Content-addressed images.** A `DeploymentRef` carries contract,
implementation, and configuration digests separately. That is finer than a
single image identity: recovery can require the exact same behavior while
still letting an operator reason about which of the three changed.

## The load-bearing property: restore before adopting candidate state

The kernel does not keep an in-memory `Execution` across a committed Step. For
each Step it runs the candidate reduction, takes a snapshot, restores a fresh
`Execution` from that snapshot, and then continues on the restored instance
(see [`tree_jobs.go`](../tree_jobs.go) and
[`step_commit.go`](../step_commit.go)).

The guarantee is specific: **every adopted candidate strategy state has passed
a successful Snapshot/Restore round trip**. A Restore error or nil Execution
prevents adoption. This exercises the strategy's restoration path during normal
execution as well as recovery.

It does not prove that the restored Execution preserves the state or behaves
equivalently. Those remain Definition obligations checked by
[`RunDefinitionConformance`](../agenttest/definition_conformance.go). Nor does
this round trip exercise deployment resolution, complete tree reconstruction,
pending Effect reconciliation, or the persistence protocol. Recovery tests
remain necessary for those boundaries.

Two obligations follow, and both are easy to violate accidentally:

- A change that skips the round trip for performance would silently convert a
  structural guarantee into a test-coverage question. Do not make the restore
  conditional.
- The current round trip processes complete strategy state on every successful
  candidate reduction, not only during recovery. See
  [Cost of the guarantee](#cost-of-the-guarantee).

This property deserves to be stated wherever the recovery model is explained,
because a reader who does not know it will misjudge both the safety and the
cost of the design.

## Replaceability and composability are different questions

The boundary is strong at replacing one implementation with another. Composing
implementations is a separate question with four distinct answers, and
conflating them causes strategy authors to reach for the wrong seam.

### Process composition: the supported form

A strategy composes another strategy by starting a child Process:
`StartChild` with an explicit `ChildSpec` carrying the exact `DeploymentRef`,
the transferred `Budget`, and the attenuated `CapabilitySet`. Completion
arrives as a Signal. This works across strategies, and it is the only
composition form the kernel adjudicates.

Each child adds strategy state, accounting, and lifecycle coordination within
the existing root tree. A child has its own limits and can be targeted by
lifecycle controls, but the root tree remains the consistency, commit, and
recovery unit. Child composition does not create independent recovery or commit
throughput. Kill still depends on in-flight work reaching its settlement or
completion boundary.

### Internal reuse and Execution nesting

Pure algorithms, value objects, and validation functions can be reused inside
one Execution. One strategy still owns the complete snapshot and Signal
consumption boundary. Such reuse needs no child Process, deployment identity,
or separate lifecycle.

Embedding independently managed Executions is a different design: the outer
Execution would need to coordinate inner transitions, Signal consumption,
Effects, failures, and restoration. That recreates runtime responsibilities
inside strategy state. A private state kind does not mechanically prevent it;
the Kernel does not recursively interpret opaque payloads. The reason to reject
it as a composition mechanism is the second lifecycle owner it introduces.

Use real child Processes when composing independently managed strategies, as
workflow does. Use ordinary internal code when there is one state and lifecycle
owner. Reusing an algorithm does not require reusing its surrounding Execution.

### Dispatcher decoration: the seam for cross-cutting concerns

`Dispatcher` is a suitable seam for rate limiting, audit, and synchronous
authorization checks around an external operation. A decorator can reuse the
same immutable request without owning or altering strategy state. It remains
responsible for any external state and lifecycle it adds.

`ReplayPolicy` belongs to the Dispatcher that performs the complete operation,
including decoration. A transparent decorator can forward the declaration
unchanged only if it preserves that operation's replay semantics. Added audit
writes or authorization side effects must also be safe under the original
`EffectRequest.ID()`. Forwarding the enum alone does not establish that safety.
Any Host-side retry must honor the same identity and settlement contract and
remain explicit at that boundary; this does not justify a framework retry layer.

Persistent human approval needs a durable wait lifecycle. A wrapper that blocks
inside Dispatch has already crossed the pending-Effect boundary; a crash can
leave an unknown settlement even if the underlying operation never began.
Interaction's existing tool-input path provides a way to request input and
checkpoint a wait identified by `WaitID` before continuing. Reuse the canonical
wait mechanism when approval must survive recovery.

A checked example of transparent decoration would make the seam discoverable
and show when it is sufficient without starting another Process.

### Strategy-internal extension: three models, one rule

The three strategies extend themselves in three different ways, and the
difference is not an inconsistency to normalize:

| Strategy | Model | Variation point |
| --- | --- | --- |
| `interaction` | Interface-driven | External system boundaries: model client, context reducer, tool shapes |
| `planning` | Algorithm-driven | A pure decision procedure over a stable problem statement (`Planner`, with `goap` as one implementation) |
| `workflow` | Data-driven | A closed vocabulary of stages validated before execution, with no plugin points at all |

The rule that explains all three: **express a variation point with the most
closed mechanism that can state it.** A closed data vocabulary can be
validated up front and cannot be extended into an unowned dependency. An
interface is justified when the variation is an external system the strategy
must not own. A swappable algorithm is justified when the problem statement is
stable enough that two implementations are comparable.

A fourth strategy should choose by that rule and record which it chose, not
copy whichever strategy it read first.

### Choose the smallest complete unit of reuse

Software can be reused at several scales. Choose the scale from the ownership
needed by the consumer, not from the size of a function or an OS metaphor.

| Need | Preferred form | Consequence |
| --- | --- | --- |
| Change a threshold or one selection rule | Explicit configuration or an existing narrow policy seam | The current state and lifecycle owner stays intact. |
| Reuse deterministic computation | A function or behavior-rich domain object inside the strategy | No new runtime state carrier or scheduling boundary. |
| Perform one external operation | An Effect interpreted by the bound Dispatcher | The operation gains the existing identity and settlement semantics. |
| Add behavior around an external call | A Dispatcher decorator that preserves the complete operation's contract | Any added side effects remain part of its replay obligation. |
| Delegate work with its own strategy state, budget, or lifecycle controls | A child Process | Isolation and coordination within the same root tree. |
| Run work that needs an independent commit and recovery unit | A separately started root tree under Host ownership | No implicit parent-child completion or atomicity across roots. |

A higher-level facade can make these compositions easier to use when it owns a
real operation and expresses its orchestration policy. It must reuse the same
domain values, validation rules, and lifecycle owners. For example, a facade
that assembles a workflow and starts its root can be useful; one that adds a
parallel child scheduler or its own wait identity cannot claim to be merely
convenience. Each underlying atomic capability keeps one canonical API.

## Test the model with one complete software lifecycle

A document-review workflow illustrates the intended division of labor. This is
a design scenario using existing primitives, not a new built-in strategy or a
claim about a particular example's behavior.

1. The Host selects and binds the workflow, review, and revision deployments
   through existing deployment and resolution contracts. It supplies model
   clients, permissions, resource bounds, and durability. Product selection and
   any catalog remain outside the Kernel.
2. The workflow starts a review child with an exact DeploymentRef and explicit
   authority and budget. The Kernel owns admission and the child's lifecycle;
   the parent strategy decides why review is needed.
3. The child declares a model request. In durable mode the Kernel acknowledges
   the pending Effect before its Dispatcher calls the model. The result reaches
   strategy state through the settlement and Signal path.
4. The child's result returns to the workflow. The workflow decides whether to
   complete or start revision. A policy change in that decision changes the
   workflow, not the Kernel's child completion semantics.
5. If an interaction child needs human input before a later tool action, it uses
   the existing tool-input and durable wait mechanism. The Host presents the
   request and submits the answer against its wait identity. No Dispatch
   goroutine becomes the durable record that approval is outstanding.
6. If the runtime stops, the Host recovers the root tree with its recorded
   deployments. Pending external work follows its declared replay policy;
   an unresolved unknown outcome is not permission to repeat the whole workflow.
7. On completion, the Host retains the product result under its own policy and
   releases the tree when runtime retention is no longer needed. Composing
   another strategy did not create another retention or recovery mechanism.

The acceptance criterion is that this lifecycle uses the same primitives for
each participating strategy. If implementing it requires the Engine to
recognize a review stage, parse a planning snapshot, or understand an approval
screen, the responsibility has crossed the wrong boundary.

## Where the analogy breaks

### State isolation, runtime failure, and external outcomes differ

An Execution owns private strategy state, but all in-process implementations
still share the Go process and its memory. Capability checks govern declared
Effects; they cannot prevent arbitrary code from opening a network connection.
Panic recovery at a call boundary does not contain every failure of the hosting
process. Strong isolation would require a separate execution boundary with its
own explicit transport, cancellation, and lifecycle requirements.

Within the supported boundary, distinguish three outcomes. A committed Process
failure is a logical result. A `RuntimeError` can stop the durable writer without
committing a terminal result for the execution. An unknown Effect settlement
means the external outcome cannot yet be established. Collapsing these into
"the program crashed" would lose the information needed to decide whether to
continue, recover the authoritative tree, or adjudicate an external operation.

Tree acknowledgment and incarnation fencing establish ownership of recorded
runtime progress. They do not undo an external tool action or automatically
revoke an old worker's access to an external system. Dispatchers still need the
identity and reconciliation guarantees their operations require. A fresh
runtime instance alone is no evidence that repeating external work is safe.

### Cancellation is cooperative

The kernel does read wall time — it timestamps jobs and measures durations.
The actual principle is narrower and correct as stated in GoDoc: **wall time
never enters strategy input**, so a Step stays deterministic and replayable,
and business time arrives as an explicit payload.

`Limits` bounds declared countable resources, not elapsed time or actual heap
use. Bounded execution is an implementation obligation. A Step that loops
forever, or a Dispatch that never returns, can hold its Process and its parent's
wait indefinitely. Host context deadlines already create termination intents;
they cannot force non-cooperating code to return.

A Step job stores a `cancel` function, and invalidation marks the job stale and
calls it. The kernel still waits for completion before releasing the job.
Snapshot and Restore have no context parameter, and Dispatch currently uses the
tree context without a separate job cancellation function. Adding a deadline
would request cooperation; it would not forcibly stop a goroutine or make
untrusted in-process code safe.

A concrete need could justify cooperative execution deadlines. Such a design
must specify the policy owner, which calls receive cancellation, how stale
candidates are discarded, when jobs are joined, and how an interrupted external
operation reaches a conclusive or unknown settlement. If expiry becomes an
observable runtime fact, its recording and recovery semantics must be explicit;
the wall clock must remain outside strategy input. The design cannot assume in
advance that all resulting state is purely in-memory.

Durability calls use `context.WithoutCancel` to detach acknowledgment from caller
cancellation. Their implementations still own bounded I/O and reconciliation
when a commit response is lost. A computation deadline must not turn an
unacknowledged commit into an assumed success or an assumed rollback.

Durable timers address a different lifecycle: preserving a future scheduled
event across recovery. They require their own identity, persistence, delivery,
and cancellation rules, as recorded in the roadmap. They do not require forced
preemption or per-job deadlines first.

### The kernel owns termination by kill

Termination by kill is not delivered as a catchable strategy event. The kernel
owns termination, discards stale candidate work, and preserves committed state
according to the durability contract. This is not forced goroutine termination
or rollback of external work: outstanding jobs still need to complete, and
external work still needs settlement.

A catchable termination event would add cleanup work with its own ownership,
boundedness, failure, and recovery rules. Revisit it only for a concrete need
that existing lifecycle and settlement mechanisms cannot express.

### There is no sibling communication and no supervision policy

The closed framework effect set is wait, start child, and wait children. A
strategy cannot address a sibling, and no Effect sends a Signal to another
Process. Communication is therefore tree-shaped: a child's result returns to
its parent, which decides what to start next. That is structured concurrency
and should stay.

Likewise the kernel has no restart or supervision strategy, which the roadmap
already rules on: agent side effects are frequently irreversible, so an
automatic restart loop is a gamble the kernel must not take on a strategy's
behalf. Child failure returns to the strategy as a fact.

Shared child-lifecycle scenarios could make the existing choices easier to
evaluate: failed, timed-out, or killed children, and children blocked by an
unknown settlement. Unknown settlement is not itself a terminal child failure.
Scenarios should verify each composing strategy's declared response while
allowing different policies; they do not justify a common supervision API or
require every strategy to compose children.

## Cost of the guarantee

The current implementations snapshot and restore complete strategy state. If
state grows linearly with progress and each round trip processes all of it,
cumulative round-trip work can grow quadratically in the number of Steps.
Interaction context reduction can bound that growth, so the relevant state sizes
depend on the consumer and its policy.

This is a cost hypothesis, not evidence of a dominant bottleneck. Measure
representative state growth, round-trip latency and allocations, and their share
of total cost alongside model calls, tool execution, and tree persistence.
Changing Host storage representation alone does not remove strategy capture
and restore work inside the Engine.

Only a measured bottleneck justifies investigating a representation whose cost
tracks changes rather than total state. Any proposal must preserve successful
restoration before candidate adoption, exact state equivalence, and the full
tree recovery contract, then demonstrate an improvement on the same workload.

## Vocabulary rules for the boundary

One domain noun names one concept across strategies, and the boundary makes
violations expensive because strategy authors read sibling packages as
examples. Two rules with current consequences:

- An observation-only tap and an input driver have distinct names.
  `interaction.ExecutionObserver` receives execution facts without control
  authority; `planning.Sensor` supplies world state to the decision procedure.
- One atomic capability has one semantic owner and canonical contract. The two
  roles above are different capabilities, so their naming collision does not
  justify a shared Observer interface. Kernel listeners already expose common
  lifecycle facts across strategies. Additional domain fact callbacks need
  their own demonstrated consumers; workflow does not need an observation
  interface merely to match another package's shape.

## Evaluate improvements against the software model

The strongest next step is to make the existing boundary easier to implement
and harder to misuse. The following questions turn the analogy into a design
test rather than a source of speculative features:

1. **Can an independently authored strategy run without a Kernel edit?** Its
   Definition, Dispatcher, state codec, and checked examples should be enough
   when its needs fit the existing primitives. A new concrete-strategy branch
   in Engine is evidence of misplaced knowledge.
2. **Can another Host bind it without understanding private state?** Product
   identity, storage access, model clients, and credentials stay at their owned
   boundaries. Hidden global registration or ambient product state makes the
   apparent software boundary incomplete.
3. **Can every piece of progress name its owner?** Strategy state belongs to
   Execution, runtime progress to the root tree, and external-operation progress
   to its Dispatcher or external system. A second mailbox, scheduler, or
   snapshot family needs a responsibility that the existing owners cannot hold.
4. **Does the proposed primitive close a lifecycle gap?** State the input,
   authority, identity, completion, cancellation, and recovery semantics, and
   show why parameterization, composition, or decoration cannot express them.
   A familiar OS feature name is not evidence of a gap.
5. **Can the improvement be proved at its boundary?** Use conformance and crash
   scenarios for contracts, a checked usage example for discoverability, and a
   representative before/after measurement for performance. Avoid introducing
   an interface whose only justification is that another strategy has one.

These tests favor a small shared execution contract with capable software above
it. They do not require package installation, dynamic Go code loading, a common
plugin manifest, an application shell, or a framework service manager. A future
consumer that needs distribution or execution isolation must supply those
requirements separately; neither is implied by implementing Definition.

## Open directions

Clarify and demonstrate existing contracts before extending the runtime. The
order below is a suggested review order, not a dependency chain or a scheduled
task. New capabilities still require a demonstrated need.

1. **Explain restore before adoption in GoDoc.** State the candidate-state
   guarantee together with the remaining Definition and tree-recovery
   obligations, without promising that every Step validates complete recovery.
2. **A minimal Definition example.** Show authorship of the narrow contract and
   run [`RunDefinitionConformance`](../agenttest/definition_conformance.go)
   immediately, so state equivalence and deterministic continuation are part
   of the example. Complete the authoring path with deployment binding and one
   managed execution; avoid a separate authoring API or plugin manifest.
3. **Exercise `ReplayPolicy` conformance.** Require evidence that repeated
   dispatch of the same immutable Effect under the original EffectID represents
   one logical operation behind a `SameIdentity` claim, including any decorator
   side effects. Local fakes alone cannot prove a remote system's guarantee.
4. **Show transparent Dispatcher decoration with a checked example.** Reuse the
   request identity and demonstrate preserved settlement and replay semantics.
   Keep persistent human approval on the durable wait path.
5. **Shared child-lifecycle scenarios.** Verify declared responses to terminal
   children and unknown-settlement waits for strategies that compose children,
   without imposing one failure policy. Extend an existing composition example
   with a focused recovery scenario to demonstrate that distinct strategies
   share one tree protocol without exposing their private state to the Host.
6. **Cooperative execution deadlines.** Consider only with a consumer requiring
   bounded waiting for cooperating implementations and an explicit cancellation,
   job completion, settlement, and recovery design. Do not promise forced
   termination of non-cooperating code.
7. **Incremental state representation.** Investigate only after representative
   measurements establish a dominant bottleneck, preserving restore before
   adoption and the complete recovery contract.
8. **Durable timers.** Follow the roadmap with a consumer supplying lifecycle
   requirements. This direction is independent of cooperative job deadlines.
