# Durable Agent runtime roadmap

Status: proposed. Recorded on 2026-09-07 against v0.15.0, commit
`b77639a810ac83ca0f6b367fc5ac4520fcff7e57`.

This document records the requested design analysis and implementation direction.
It does not change the current API or promise that the proposals are implemented.
Package contracts remain in [GoDoc](../doc.go) and checked examples. Update or
remove resolved proposals as implementation lands rather than maintaining a
second description of the runtime.

## Direction

Strengthen the existing durable state-machine kernel around explicit ownership,
durable acknowledgment, recovery, and observation. Preserve the small
`Definition` / `Execution` boundary instead of introducing another runtime or a
general application platform.

Three bodies of work provide useful design principles:

| Reference | Principle to apply | Scope interpretation |
| --- | --- | --- |
| Erlang/OTP | Isolate process lifecycles, make state transitions explicit, and separate supervision from business behavior. | A Process owns strategy state; an active runtime instance has a lifecycle distinct from the logical execution it hosts. Restart policy stays explicit. |
| Temporal | Record execution progress and inputs so recovery has a defined meaning; isolate external work from deterministic workflow decisions. | Preserve pure candidate Steps and explicit Effects, and close gaps between input acknowledgment and recovery state. |
| Databases | Define transaction boundaries, serialize competing writers, and acknowledge only the durability actually established. | Keep the root tree as the consistency and recovery unit, with conditional publication and incarnation fencing. |

OTP supervision specifies restart strategies and bounded restart intensity; it
does not by itself provide durable business execution. Borrow lifecycle
separation without automatically adding supervisor strategies or restart loops
to the Agent kernel. See the official [supervision principles](https://www.erlang.org/doc/system/sup_princ.html)
and [state-machine behavior](https://www.erlang.org/doc/system/statem.html).

Temporal reconstructs workflow progress from recorded history and constrains
workflow decisions to remain deterministic. Scope already uses explicit
snapshots and effect boundaries, so adopting Temporal's entire replay engine is
not a prerequisite for correctness. See [workflow execution](https://docs.temporal.io/workflow-execution)
and [workflow definitions](https://docs.temporal.io/workflow-definition).

Database WAL and transaction isolation explain why acknowledged state and
concurrent ownership need precise storage guarantees. The Host's database
implementation supplies those guarantees; the Agent protocol describes what
must be atomic. See PostgreSQL's [WAL introduction](https://www.postgresql.org/docs/current/wal-intro.html)
and [transaction isolation](https://www.postgresql.org/docs/current/transaction-iso.html).

## Foundation to preserve

The current runtime already has the important structural boundaries:

- `Definition` owns immutable behavior and creates or restores `Execution`.
  The kernel treats strategy payloads as opaque, bounded JSON.
- A Step computes a discardable candidate. Model calls, tools, and other
  external operations are Effects outside the Step.
- Each root tree has one serial commit owner. Sibling computation and external
  work can run concurrently; authoritative tree commits remain serialized.
- Effects advance through a prepared frontier of planned, pending, and settled
  work. Durable mode acknowledges pending state before dispatching the Effect.
- External Signal admission acknowledges the durable mailbox and budget
  publication. Conflicting identity reuse is rejected.
- Stable effect identity and explicit replay capability govern redelivery.
  Unknown outcomes require explicit treatment; they are not assumed successful
  or silently retried.
- `TreeSnapshot` is the recovery unit. Exact `DeploymentRef` resolution and
  `TreeIncarnationID` fencing prevent ambiguous behavior selection and stale
  writer publication.
- A stopped durable writer reports `RuntimeError` separately from a committed
  logical `Result`, retaining its last acknowledged head and unresolved Effect
  identities.
- OpenTelemetry remains an external integration. Host storage, product routing,
  deployment catalogs, and operational policy remain outside the kernel.

The key distinction is: a root tree owns transactional consistency, a Process
owns strategy state and logical lifecycle, and an incarnation identifies an
active writer. Improvements should make those boundaries more precise.

## 3. Scope observation to the active incarnation

### Current evidence

[`otel/agent/observer.go`](../../otel/agent/observer.go) indexes Process spans by
`ProcessID`, Step spans by Process identity and sequence, and Effect spans by
`EffectID`. Those identities can recur during restoration.

A public-API probe shared one Observer between an original Engine and an Engine
restoring the same tree. While the original external operation remained in
flight, the restored Process was still running but its new activation span had
already ended: the Observer treated the repeated Process identity as a
duplicate. This is a concrete overlap bug, independent of adding new metrics.

### Proposed change

Use the existing incarnation identity to separate active observation records
for Processes, Steps, and Effects. Carry that identity through the dispatcher
observation boundary where needed. Keep it separate from effect idempotency:
changing the Effect's deduplication identity on restore would create a new
external operation instead of resuming the same logical one.

Add storage observation through an external `TreeDurability` decorator when
implementing the durable path. Useful measurements include commit duration,
snapshot size, ownership conflicts, durable-head age, and unresolved-effect
age. Use high-cardinality identities as trace attributes rather than metric
labels, and avoid exporting signal or model content by default.

The existing observer callback runs synchronously on the owner line and must
stay bounded. Keep exporter I/O outside it; do not blindly make all observation
asynchronous because dispatcher span association currently depends on ordered
event handling. Telemetry remains diagnostic rather than a recovery log.

### Acceptance criteria

- Old and restored instances sharing an Observer have independent span
  lifecycles, including overlapping dispatch and late completion.
- Finishing one incarnation cannot close another incarnation's spans.
- Restoring observation identity does not change external idempotency keys.
- Durability metrics and traces distinguish attempted, acknowledged, rejected,
  and unresolved operations without importing OpenTelemetry into Agent.

## Framework and application storage boundary

Scope owns `TreeDurability`, the memory reference implementation, and
[`RunTreeDurabilityConformance`](../agenttest/tree_durability_conformance.go).
The shared suite covers admission acknowledgment, competing activation, stale
writers, commit-response loss, and recovery across protocol boundaries.

Database selection, production adapters, transaction configuration, and
integration tests against a concrete database belong to the consuming
application or its independently owned adapter. They are not implementation
items for this framework roadmap. Adapters can reuse the shared conformance
suite and supplement it with evidence from their own storage environment.

## 5. Bound long-lived state before adding long-lived execution features

The current runtime has finite budgets and snapshot size limits. Its mailbox
retains consumed records, deduplication identities, and wait history; a tree
retains descendants until release. Lifetime counts and eventual wire-size
checks are not a complete policy for indefinitely running executions.

For a demonstrated long-running use case, specify payload retention, input byte
admission, deduplication retention, and a bounded execution lifetime together.
An explicit Host-controlled handoff to a new execution may be easier to explain
than an indefinitely growing tree. Do not prune unresolved Effects or discard
deduplication facts while their guarantees still apply.

Temporal's [Continue-As-New](https://docs.temporal.io/workflow-execution/continue-as-new)
is useful inspiration for separating logical continuity from one execution's
bounded history. It is not a requirement to add a second lifecycle or API now.

Durable timers also require a concrete design: persist the scheduled deadline
and delivery identity, and recover wakeup delivery after restart. Despite the
current overview mentioning timer effects, the implemented closed effect set
does not provide a durable timer facility. Host scheduling and a recorded
reached-deadline intent do not by themselves preserve a future timer.

Measure representative snapshot growth, commit latency, recovery time, and
queueing before changing storage representation or scheduling. Existing
benchmarks provide a starting point; no production measurement in this review
established a dominant bottleneck. Incremental logs, per-Process commits, and
additional indexing therefore remain unproven changes.

## 6. Extend through the existing execution boundary

Interaction, planning, and workflow should continue to express their behavior
through the same `Definition`, `Execution`, Signal, and Effect contracts. Keep
strategy-specific state transitions and validation with the strategy owner;
keep Engine scheduling and recovery semantics shared.

The existing
[`RunDefinitionConformance`](../agenttest/definition_conformance.go) checks
descriptor stability, isolated starts, deterministic Steps, and snapshot/restore
behavior. Extend representative multi-step and recovery cases when a new
strategy needs them. Prefer that evidence over new plugin hooks, registries,
generic parameters in the kernel, or parallel composition runtimes.

## Implementation order and boundaries

1. Fix Observer incarnation isolation as its own change. Add external durability
   observation at the existing `TreeDurability` protocol boundary.
2. Extend framework conformance tests when a concrete contract gap is found.
3. Add long-lived execution or timer capabilities only when a concrete consumer
   supplies the lifecycle requirements. Optimize only after measurement.

Use one canonical API per atomic capability and one strict current schema.
Replace superseded designs outright, including their consumers and tests;
do not introduce compatibility aliases, schema-version envelopes, migrations,
or dual persistence paths. Discuss breaking changes before applying them.

The evidence above came from source inspection, focused existing durability,
incarnation, mailbox, and signal tests, and temporary public-API probes. The
probes restored the last acknowledged memory-store head; they did not perform
an operating-system crash or exercise a real SQL database. Concrete database and
operating-system crash validation belongs to downstream adapter integration tests.
