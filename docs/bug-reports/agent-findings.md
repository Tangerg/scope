# Agent module — open findings

Verified against `HEAD = 332a0450e` ("build: align consumers with durable agent
settlements") with Go 1.27.0 on darwin/arm64. Every entry below was re-traced at
that revision; nothing in this document is carried over on trust.

## Scope and method

Audited: the `agent` module root package (the execution kernel), its
`strategy/` packages, `agenttest`, `messaging`, `internal/`, and `examples/`.
Provider adapters (`models/*`, `vectorstores/*`, `historystores/*`) are out of
scope. The standards applied are `AGENTS.md`, `PROJECT_RULES.md`,
`DESIGN_PHILOSOPHY.md`, `REFACTORING.md`, and the kernel contract in
`agent/doc.go`.

Checks run at this revision (read-only; no repository file was changed):

| Check | Result |
|---|---|
| `MODULE=agent FAST=1 scripts/check.sh build vet test` | pass |
| `MODULE=agent FAST=1 scripts/check.sh race` | pass |
| `MODULE=agent scripts/check.sh lint` | pass |
| `go test -run '^$' -bench . -benchtime=1x ./...` | pass |
| `go test -run '^$' -bench Benchmark ./` (root package, default benchtime) | **FAIL** — finding M1 |

Findings that fail a gate are called out; the rest are reachable through
passing checks, which is why they survived. Three findings (H1, M2, L1) were
reproduced with a standalone program kept outside the repository (a scratch
module in `/tmp`), and those reproductions are quoted verbatim.

Severity: **high** — a documented-legal Host operation destroys an execution, or
the published contract is wrong. **medium** — legal input produces a wrong
outcome or classification, or an existing guard cannot do its job. **low** —
ownership, diagnosability, or coverage defect with no wrong outcome today.

Class: **confirmed defect** · **standard violation** (repository rule, no wrong
outcome today) · **coverage gap** (protected behavior that no check protects).

## H1. Unaddressed Strategy input is admitted and then kills the Process in every strategy that does not own that input vocabulary

**Symbols.** Kernel contract: `agent/process.go:63`, `agent/doc.go`
("Unaddressed Strategy input can also queue while Paused or waiting for children
without releasing either state"). Admission: `agent/mailbox.go:123-152`,
`agent/mailbox.go:358-397`. Window construction: `agent/tree_runtime.go:1763`,
`agent/mailbox.go:241-253`. Positional reads:
`strategy/workflow/execution.go:49,141,154,156,162,390,395,436,439,461,464`,
`strategy/planning/protocol.go:163-170`,
`strategy/planning/execution.go:35,231,238,244`,
`strategy/coordination/deadline.go:118,128-134`,
`strategy/coordination/first_success.go:155,158,161,183,186,204,207`,
`strategy/collaboration/execution.go:21,74,116,141,199-202`,
`examples/composition/main.go:319-331,389,441`. Reference implementation:
`strategy/interaction/child_signals.go:9-30`,
`strategy/interaction/execution.go:385-448`.

**What is wrong.** `Process.DeliverSignals` documents that unaddressed input
"queues for the next Strategy-safe Step, including while Paused or waiting for
child completion", and the mailbox admits it in `Running`, `Paused` and
`Waiting` (`Waiting` rejects it only while an *external* wait is current —
`agent/mailbox.go:388-397`). The Step window is the mailbox suffix in arrival
order, so an admitted unaddressed Signal precedes the protocol frame the
strategy is waiting for.

Four strategies and one shipped example read their protocol frame by position
(`signals[0]`, or a window prefix counted from index 0) and reject a window that
contains anything else with a raw error. The kernel maps a raw Step error to
`FailureKindExecution` + `execution.step.failed`, so a documented-legal Host
delivery terminates the whole Process and the diagnostic blames the strategy's
protocol.

The root cause has a second half: the kernel promises admission and later
consumption for input whose vocabulary the strategy never declared, and the
execution protocol gives a strategy no way to say "not mine, leave it queued" —
the only outcomes are consume, ignore (which re-runs the same Step with the same
window), or fail. `Transition.ConsumedSignals` is "the length of the delivered
Signal prefix to commit" (`agent/transition.go:125`) against a single monotonic
`signalCursor` (`agent/mailbox.go:281`), so consumption is prefix-only: no
strategy can leave `signals[0]` queued while consuming `signals[1]`.
Whether the fix belongs to admission or to the strategies is
therefore a contract decision, not a local bug fix; the two options are stated
under *Fix layer*. `interaction` shows that the strategy-side shape is
implementable (it collects by decoded operation and consumes its steer
vocabulary), but it too fails on a payload outside its vocabulary
(`strategy/interaction/child_signals.go:9-20`,
`strategy/interaction/execution.go:402-448`), so neither reading is complete
today.

**Root cause, stated as the missing fact.** `Descriptor` declares
`InputSchema` and `OutputSchema` and nothing about Signals
(`agent/descriptor.go:DescriptorConfig`, accessors at `descriptor.go:89-115`).
Every other Host-supplied value crossing into a Strategy is validated against a
declared contract before it is adopted, and rejection returns an error to the
caller:

| Boundary | Contract the kernel can check | Rejection |
|---|---|---|
| Start input | `Descriptor.InputSchema` via `ValidateInput` (`child_start.go:49`) | `Engine.Start` returns an error |
| Unknown-effect adjudication | the prepared Effect frontier, checked by `processState.prepareResolution` before `adoptCandidate` (`tree_runtime.go:commitResolution`) | `Process.ResolveUnknownEffect` returns an error |
| **Signal delivery** | **none exists** | **none — the Step fails and the Process terminates** |

`ResolveUnknownEffect` is the in-module proof that the correct shape is
reachable: it validates a Host-supplied value against the Strategy's current
contract, replies with the error, and mutates nothing. Signal delivery is the
one boundary where the kernel has no declared contract to check, so it validates
only the envelope — identity, size, wait addressing — and admission reports
`accepted=true` for a payload that will destroy the Process one Step later. The
defect is not that a strategy rejects input it does not understand; it is that a
rejectable Host call is reported as accepted and then escalated to an
unrecoverable Process termination.

**Deliberate rejection versus accidental misdecode.** Three sites reject
unsolicited input with an authored message, so that behaviour is an intentional
design choice, not an oversight:

```
strategy/workflow/execution.go:50      "Stage %q does not accept unsolicited Signals"
strategy/planning/execution.go:36      "planning: initial sensing does not accept Signals"
strategy/coordination/deadline.go:119  "deadline does not accept external Signals"
```

The child-waiting phases are different: they decode the unaddressed payload as
their protocol frame and fail inside that decode, so the diagnostic names a
frame that is in fact intact. The reproduction below is this second kind. A
repair must keep the first group's intent (reject, but as a declared classified
outcome) and fix the second group's misattribution.

**An unfulfilled obligation, with no guard.** `agent/doc.go:212` requires that
"Each strategy declares its own safe consumption boundary and **proves it with
contract tests**". No `strategy/*/doc.go` states a consumption boundary, no
strategy has such a contract test, and no architecture guard checks that the
obligation was met. That is why five strategies could diverge on the same
question without any gate noticing.

**Evidence — reproduced (`workflow`).** A two-Stage `workflow` Definition
(`Transform` → `Transform`), an ephemeral `Engine`, and one unaddressed
SignalRequest delivered through the public API:

```
unaddressed input accepted=true err=<nil>
FINAL status=failed awaitErr=<nil> failed=true kind=execution code=execution.step.failed
      message="workflow: invalid protocol payload: Stage \"first\" does not accept unsolicited Signals"
joinErr=<nil>
```

The same shape is reachable in every phase that expects a frame, and while
waiting for a child: `planning` requires exactly one Engine-owned Signal
(`protocol.go:163-170`) and its ready phase rejects any Signal
(`execution.go:35`); `coordination.FirstSuccess` decodes start results from
position 0 and wait frames from `signals[0]`; `coordination.Deadline` requires
exactly one Engine-owned timer settlement; `collaboration` decodes all four of
its handshake frames from `signals[0]`/`signals[consumed]`; the `composition`
example does the same. None of these packages documents a refusal of Host input,
and each `doc.go` describes ordinary Engine composition.

`examples/composition/main_test.go:340-346` pins the wrong distinction: its
"rejects unexpected first signal" case prepends a zero-valued `Signal` (an
invalid frame), not an admitted unaddressed one, so it freezes rejection of
malformed input as if it were rejection of legal input.

**Fix layer.** Decide the contract once, then apply it everywhere:

- *Strategy side:* select the protocol frame by identity and decoded kind
  (`Signal.EngineOwned`, wait identity, frame key) exactly as the shared
  `childcall` state machine already validates it, and classify unrecognized
  input with a strategy-owned `agent.Fail(consumed, failure)` code instead of
  returning a raw error. `childcall` should own the window-entry rule for the
  single-child handshake (see L2) so the two strategies cannot disagree.
- *Kernel side:* if a strategy may declare that it accepts no unaddressed input,
  that declaration belongs to the frozen contract (`Descriptor`/`Deployment`)
  and admission must reject the delivery with `ErrSignalRejected` before the
  mailbox changes. Then `Process.DeliverSignals` keeps its promise and the
  strategies keep their protocol.

Either choice needs `agent/doc.go` and the affected `strategy/*/doc.go` to state
the safe-consumption boundary the kernel already asks for ("Each strategy
declares its own safe consumption boundary and proves it with contract tests").

**Missing coverage.** One engine-level regression per built-in strategy (and for
`examples/composition`): drive the Process to a frame-waiting phase, deliver one
unaddressed Signal through `Process.DeliverSignals`, and assert either
`ErrSignalRejected` at admission or the strategy's declared failure code — never
`execution.step.failed`. `strategy/workflow`, `planning`, `coordination` and
`collaboration` currently contain no test that delivers an unaddressed Signal to
the strategy's own Process.

## H2. A Step cannot discard its work and classify the fault, so the discarding exit is always `FailureKindExecution`

This is the shared root cause of H1, M3 and M4. Repairing those three
separately re-implements the same missing capability three times.

**Symbols.** Discarding exit: `agent/tree_runtime.go:2271-2280`
(`failureKindForError(result.err, FailureKindExecution)`,
`failureCodeExecutionStepFailed`). Classifying exit:
`agent/step_commit.go:145-151` (`TransitionKindFail`). Classification helper:
`agent/execution_boundary.go:failureKindForError`. Vocabulary:
`agent/failure.go:17-31`.

**What is wrong.** A Step has two ways to end badly, and they differ in two
independent dimensions at once:

| Exit | Candidate state | Input consumption | Can declare a `FailureKind` |
|---|---|---|---|
| `return Transition{}, err` | discarded whole | does not advance | **no** — always `FailureKindExecution`, unless the error is a `*CallbackPanicError` |
| `agent.Fail(consumed, failure)` | adopted | advances | yes — kind, code and message are preserved |

The two dimensions are not independent in the API. A Strategy that detects a
contract violation it must not commit — a malformed protocol frame, an input it
cannot interpret, a Host-supplied payload outside its vocabulary — wants to
discard the Step *and* report `FailureKindContract`. There is no exit for that.
It must either commit consumption of the very input it rejected, or accept
`FailureKindExecution` for a fault that is not an execution fault.

The result is that the same fault category is classified differently depending
on which code path detects it, inside one strategy. `planning` reports a
protocol violation as `FailureKindContract` through its `fail` helper at seven
sites (`planning/execution.go:84,117,134,140,171,177,209`) and as
`FailureKindExecution` through a raw `ErrInvalidExecutionState` return at four
others. `collaboration` returns its own `ErrInvalidProtocol` raw seven times, so
none of those reach `FailureKindContract` (M3). Across the five built-in
strategies the discarding exit is chosen roughly twenty times more often than
the classifying one:

| Strategy | `return Transition{}, err` exits | `agent.Fail` exits |
|---|---|---|
| `interaction` | 83 | 3 |
| `workflow` | 56 | 5 |
| `coordination` | 41 | 1 |
| `collaboration` | 40 | 3 |
| `planning` | 36 | 1 |

Not all 256 are wrong: `ctx.Err()` must propagate as an error so cancellation is
classified by intent rather than by the Step, and a genuine internal invariant
break is an execution fault. The defect is that the API offers no way to
distinguish the two, so the choice is made by convenience and the `FailureKind`
vocabulary loses its meaning — `execution` becomes the default for everything
that is not a panic.

This is also why H1's diagnostic blames the strategy. The strategy correctly
detected a Host contract violation; the only exit that discards the Step reports
it as `execution` / `execution.step.failed`, which reads as "the strategy
failed".

**Fix layer.** The kernel's Step contract. The discarding exit needs to carry a
`Failure`, in one of two shapes:

- a typed error the kernel recognises, so
  `return Transition{}, agent.ContractViolation(failure)` discards the candidate
  and commits the declared kind and code — the same mechanism
  `*CallbackPanicError` already uses to reach `FailureKindPanic`; or
- a `Transition` kind that fails without advancing consumption, making the two
  dimensions independent in the constructor set.

The first is smaller and reuses a proven pattern. Either way `failureKindForError`
stops being the only classifier of strategy faults, and H1, M3 and M4 become
call-site changes rather than three separate contract decisions.

**Missing coverage.** No test asserts the `FailureKind` produced by a raw Step
error; `failure_contract_test.go:76` enumerates the four kinds as values without
binding any to an exit shape. A regression case should drive one strategy
protocol violation through each exit and assert the kinds differ today, so the
repair has a failing test to satisfy.

## M1. `BenchmarkWaitingTreeCapture` fails at default benchtime, and no gate runs benchmarks

**Symbols.** `agent/snapshot_work_benchmark_test.go:13-28` (`b.Loop()`
increments `root.committedSteps` every iteration), `:58` (fixture grants
`Budget.Steps = count*10+100`), `agent/process_snapshot.go:336-341`
(`validateContract` budget containment), `scripts/check.sh:55`
(`CHECKS=(build vet test tidy pinned-test lint vuln)` — no benchmark step).

**What is wrong.** The benchmark measures whole-tree capture but advances a
committed Step per iteration, so the fixture's Step budget is exhausted long
before default benchtime ends and `captureTree` returns
`agent: invalid process snapshot: incomplete Process identity or state`. The
repository gate never runs benchmarks, so the only benchmark that measures the
fixed cost of every durable boundary cannot report a number.

**Evidence.**

```
--- FAIL: BenchmarkWaitingTreeCapture/processes_1
    snapshot_work_benchmark_test.go:24: agent: invalid process snapshot: incomplete Process identity or state
--- FAIL: BenchmarkWaitingTreeCapture/processes_10
--- FAIL: BenchmarkWaitingTreeCapture/processes_100
--- FAIL: BenchmarkWaitingTreeCapture/processes_1000
```

`-benchtime=1x` passes (one iteration does not exhaust the budget), which is why
the defect survived. Related costs are already measured in the suite:
`BenchmarkTreeOwnerTraversal/capture` reports 60.5 µs/121 KB at 15 Processes,
260 µs/536 KB at 63, and 1.07 ms/2.5 MB at 255 on this machine.

**Secondary defect at the same site.** `process_snapshot.go:336-341` folds nine
independent contract checks — identity, deployment reference, start time, status,
committed state, `Limits`, `TreeLimits`/capabilities and budget containment —
into one message, `incomplete Process identity or state`. That is why budget
exhaustion reads as a malformed identity. Split the message per invariant (or
return the specific cause) so the failure names itself.

**Fix layer.** The benchmark fixture (do not advance committed Steps per
iteration, or size the budget for the iteration count), `scripts/check.sh` (run
`-benchtime=1x` as a smoke gate), and the `validateContract` message split.

## M2. `planning.NewDefinition` accepts a quota that cannot admit one Action, and the execution then rejects its own output

**Symbols.** `strategy/planning/definition.go:38-39,63-91` (no
`MaxActionAttempts` validation), `strategy/planning/execution.go:106-108`
(`!Allows(attemptCount, 1)` → `complete`), `strategy/planning/state.go:178-202`
(`complete` → `validate` → `output`), `strategy/planning/output.go:121-123`
(`OutcomeUnreachable` requires `passes == 1`), sibling precedent
`strategy/workflow/loop.go:62`.

**What is wrong.** `agent.NewQuota(0)` is a finite zero bound, distinct from the
zero value that means unlimited, and `NewDefinition` accepts it. On the first
observation that does not satisfy the Goal, `acceptSense` takes the exhausted
limit branch and completes; `output()` derives `OutcomeUnreachable` with
`PlanningPasses == 0` because the Planner was never called; `Output.Validate`
requires exactly one pass for that outcome. The Step returns an error, so the
Process fails with an internal contract violation for a configuration mistake.
`workflow.Loop` and `coordination.FirstSuccess` reject the equivalent unusable
bound at construction, so the strategies disagree about where a configuration
error is owned.

**Evidence — reproduced.** `NewDefinition` returns no error for
`MaxActionAttempts: agent.NewQuota(0)`; running that Definition against a sensor
whose first observation is false yields:

```
run err=<nil>
status=failed failed=true kind=execution code=execution.step.failed
  message="planning: invalid execution state: completion: planning: invalid result:
           unreachable output requires one initial planning pass and no attempts"
```

Existing tests cover only `NewQuota(1)` (`execution_test.go:162-196`) and the
restored-limit path (`restore_test.go:180`); nothing covers the zero bound, and
`planning` has no `ErrInvalidDefinitionConfig` table.

**Fix layer.** One owner must decide what "zero Action attempts" means: either
reject a finite bound that cannot admit one attempt in `NewDefinition` (matching
`workflow.Loop`), or make `output()` and `Output.Validate` agree that a
zero-attempt, zero-pass completion is legal. Changing only one side recreates
the contradiction. M2 is one instance of the class in M2b; repair them together.

## M2b. `Quota` can express a finite zero for progress-gating dimensions, and eight of eleven owners never reject it

**Symbols.** `agent/quota.go` (`NewQuota`, `Allows`), and every `Quota`-typed
configuration field:

| Field | Rejects a finite zero | Where |
|---|---|---|
| `TreeLimits.MaxTreeProcesses` | yes | `resource.go:251` |
| `goap.PlannerConfig.MaxGeneratedNodes` | yes | `planning/goap/planner.go:54` |
| `workflow.LoopConfig.MaxIterations` | yes | `workflow/loop.go:62` |
| `planning.DefinitionConfig.MaxActionAttempts` | **no** | M2 |
| `interaction.DefinitionConfig.MaxModelCalls` | **no** | regression, see below |
| `collaboration.DefinitionConfig.MaxTurns` | **no** | `collaboration/execution.go:39` |
| `collaboration.DefinitionConfig.MaxTasks` | **no** | `collaboration/state.go:481` |
| `goap.PlannerConfig.MaxExpansions` | **no** | `planning/goap/planner.go:40` |
| `Limits.Budget.{Steps,Effects,Signals}` | **no** | `resource.go:validate` checks only `MaxPendingSignals` |
| `Limits.MaxSnapshotBytes`, `TreeLimits.MaxSnapshotBytes` | **no** | same |
| `TreeLimits.MaxChildren` | **no** | a finite zero may be intended (leaf-only tree); undocumented either way |

**What is wrong.** `Quota`'s zero value means unlimited and `NewQuota(0)` means
a finite maximum of zero. For a dimension that gates progress, a finite zero
makes progress impossible, so the invariant "this quota must admit at least one
unit" belongs to the value. It is not expressible in the type, so every owner
must re-derive it, and eight owners did not. The three that did each wrote their
own check, which is why nothing detects a missing one.

The consequences are inconsistent, which is the evidence that no owner holds the
rule:

- `planning.MaxActionAttempts` → the Strategy rejects its own Output (M2).
- `collaboration.MaxTurns` → `startTurn` returns a bare `ErrTurnLimit`
  (`collaboration/execution.go:40`), which the kernel flattens to
  `execution.step.failed` (M3).
- `interaction.MaxModelCalls` → a clean classified failure
  (`interaction/execution.go:78-85`, `interaction.limit.model_calls`).
- `Limits.Budget.Steps` → a clean kernel failure
  (`process_state.go:401`, `engine.limit.steps`), but `NewEngine` accepted a
  configuration under which no Process can ever commit one Step, contradicting
  its own stated reason for validating up front: "a defect discovered after
  Processes exist has no safe remedy" (`engine.go:142-145`).

**Regression, with its commit.** `interaction` used to reject this value.
Commit `8f4fb328c` ("agent!: make cumulative execution quotas host-controlled")
migrated the field from `uint32` to `agent.Quota` and deleted the check without
replacing it:

```diff
-	// MaxModelCalls bounds model Effects in one Interaction. It must be positive.
-	MaxModelCalls uint32
-	if config.MaxModelCalls == 0 {
-		return nil, fmt.Errorf("%w: MaxModelCalls must be positive", ErrInvalidDefinitionConfig)
+	// MaxModelCalls bounds model Effects in one Interaction. Its zero value is unlimited.
+	MaxModelCalls agent.Quota
```

The migration was right that "zero means unlimited" must no longer be rejected,
and wrong to drop the finite-zero case with it. `TestDefinitionRejectsZeroModelCallLimit`
was deleted in the same commit and `TestDefinitionAcceptsUnlimitedModelCalls`
added, so the surviving test pins only the half that still works.

**Fix layer.** `Quota` owns the distinction, not eleven call sites. Give the
progress-gating case one representation — a validating accessor or a distinct
constructor that a gating dimension must use — then delete the three hand-rolled
`Allows(1)` checks. Add a guard test that enumerates every `Quota`-typed
exported configuration field and fails unless its owner declares a zero policy,
so a new quota cannot be added without answering the question.

**Missing coverage.** No package tests `NewQuota(0)` for any of the eight
unvalidated fields. `TreeLimits.MaxChildren`'s intent is undocumented, so the
guard must record a decision for it rather than assume one.

## M3. `collaboration` loses every failure classification at the Step boundary

**Symbols.** `strategy/collaboration/execution.go:40,43,96,182-186,193,235`;
`strategy/collaboration/errors.go:5-11`; `strategy/collaboration/doc.go:16,20-21`;
contrast `strategy/interaction/execution.go:118-131` and its stable codes,
`agent/failure.go:88-93`.

**What is wrong.** `ErrTurnLimit`, `agent.ErrCounterExhausted`,
`ErrInvalidDecision` and `ErrInvalidProtocol` are returned as bare Step errors.
The kernel classifies every non-`CallbackPanicError` Step error as
`FailureKindExecution` + `execution.step.failed` (`agent/tree_runtime.go:2279`), so the Host-visible
`Result` cannot distinguish "the turn bound the Host configured was reached" from "the
coordinator broke its protocol" from "a representable counter overflowed" —
only the message text differs, and matching text is exactly what
`agent.Failure` exists to avoid. `collaboration/doc.go:16` promises that an
exhausted turn bound fails the collaboration but names no code, while the sibling
`interaction` strategy classifies the same class of condition with stable codes
(`interaction.limit.model_calls`, `interaction.model.failed`).

**Evidence.** `MaxTurns: agent.NewQuota(1)` reaches `startTurn` for turn 2 and
returns `ErrTurnLimit`; the observed Process failure is `kind=execution`,
`code=execution.step.failed`, `message="collaboration: turn limit reached"`.
`strategy/collaboration/contracts_test.go` asserts
`errors.Is(stepErr, ErrInvalidProtocol)` at the Step level only; no test asserts
a Host-visible `Failure.Kind()`/`Code()`.

**Fix layer.** `collaboration`: return `agent.Fail(consumed, failure)` with
strategy-owned codes (for example `collaboration.limit.turns`,
`collaboration.decision.invalid`, `collaboration.counter.exhausted`) and add
end-to-end assertions. `agenttest`'s Definition conformance suite is the natural
home for the shared rule that a strategy-owned failure must keep a stable
classification.

## M4. `interaction` classifies provider-data defects as Strategy execution failures

**Symbols.** `strategy/interaction/execution.go:142-145` (`validatedToolCalls`
error returned raw), `:170-176` (missing finished assistant message),
`strategy/interaction/state.go:282-298` (duplicate tool-call ID), contrast
`execution.go:118-131` (`interaction.host.failed` / `interaction.model.failed`,
`FailureKindExternal`).

**What is wrong.** In one function, provider failures are classified
(`FailureKindExternal` with stable codes) while two neighbouring provider-data
defects are returned as raw errors, so the Host sees
`execution.step.failed`/`execution` for both. The Host cannot separate "the
provider returned something invalid" (a protocol or external defect worth
reporting, or retrying under policy) from "the Strategy is broken".

Two concrete triggers exist: a response whose parts reuse one tool-call ID
(`core/chat/message.go:110-132` validates parts but not ID uniqueness, and the
interaction Dispatcher validates and settles such a response), and a completion
with a valid `finish_reason` and `Message == nil` (`core/chat/output.go:135-152`
explicitly allows it), which reaches `acceptFinalModelResponse` and fails the
Process.

**Fix layer.** `interaction`: route both through `e.fail` with a stable code and
an explicit kind (`interaction.model.invalid_response`, `FailureKindExternal`
or `FailureKindContract`, decided once), and document whether the model gets a
retry. Missing coverage: an e2e case for each trigger asserting
`Failure.Kind()`/`Code()`.

## M5. `agenttest` discards typed panic evidence and returns unbounded text

**Symbols.** `agenttest/definition_conformance.go:365,378,394,412,425`; kernel
owner `agent/callback_panic.go`, `agent/execution_boundary.go:15-60`,
`agent/failure.go:88-93`, `agent/diagnostic.go:23-35`.

**What is wrong.** The kernel routes every Host callback panic through the
exported `*agent.CallbackPanicError` (which preserves the recovered value and
`Unwrap`s an error), and `failureKindForError` maps only that type to
`FailureKindPanic`. The conformance suite that third-party Definition authors
run formats the same panics as untyped text
(`fmt.Errorf("agenttest: Definition.Start panicked: %v", recovered)`), so a
conforming author gets an error that cannot be matched with `errors.As`, loses
the original cause, and can exceed the kernel's own 4 KiB diagnostic bound.
Nothing in `agenttest` exercises a panic path, so the representation is
unprotected.

**Fix layer.** `agenttest`: return
`&agent.CallbackPanicError{Operation: ..., Value: recovered}` (bounded through
`agent/internal/panicinfo` when a message is retained) and add panic cases.

## M6. The durability conformance suite never requires a Host to reject a conflicting duplicate

**Symbols.** `agenttest/tree_durability_conformance.go:318-323` (`retry`
re-submits the identical boundary), `agenttest/tree_durability.go:162-183` (the
reference rule: a duplicate is accepted only when content is identical **and**
the head still matches), contract `agent/tree_durability.go:295-311`.

**What is wrong.** The only duplicate-commit exercise re-sends a byte-identical
boundary while the head still equals the proposal. No scenario re-submits a
captured boundary after the head has advanced, and none submits a same-key
boundary with different content, so the conflict half of the rule is never
required of a third-party Host. A Host whose fact lookup answers "already seen"
without checking content or head passes the entire crash matrix while silently
accepting a divergent commit in production.

**Evidence.** `agent.ErrDurabilityConflict` appears only inside the reference
`MemoryTreeDurability`; no conformance case asserts it through a driver.

**Fix layer.** `agenttest`: capture a boundary in the probe, re-submit it after
the head advances, and require a non-nil error, plus a same-key/different-content
case.

## L1. `Engine.Start` performs Host admission under an already-canceled context

**Symbols.** `agent/engine.go:262-330` (no `ctx.Err()` check before
`reserveProcessStart`/`requestProcessAdmission`), contrast
`agent/process.go:35` ("already canceled before submission never admits a
command") and `agent/engine.go:534,552,593,615` (tree operations check first).

**What is wrong.** Every other admission path checks `ctx.Err()` before it
changes anything. `Start` reserves a Process identity, calls the Host's
`ProcessAdmitter`, and only then fails inside initialization, reporting
`agent: initialize Process: validate initial Execution state: context canceled`
— so a canceled request produces Host-visible admission side effects and an
error that blames initialization.

**Evidence — reproduced.** With an already-canceled context and a counting
`ProcessAdmitter`, `Start` returns that error and the admitter was invoked once.

**Fix layer.** `agent`: check `ctx.Err()` at the top of `Start` and state the
contract in GoDoc.

## L2. `childcall` owns the single-child handshake but not its window-entry contract

**Symbols.** `strategy/planning/execution.go:220-222`
(`len(signals) == 0 || phase != childcall.AwaitingOpening && len(signals) != 1`)
versus `strategy/workflow/execution.go:141` (`len(signals) == 0` only);
`strategy/internal/childcall/single.go:52,84,102` (frame parsing).

**What is wrong.** Both strategies share the same handshake state machine, but
each re-implements the window-entry rule with different strictness: `planning`
requires exactly one Signal outside `AwaitingOpening`, `workflow` accepts any
non-empty window. One phase of one strategy admits a window the other rejects,
for the same shared state machine, and both decisions are positional (H1). The
rule belongs beside the phase transitions `childcall` already owns, which is
also what the H1 fix needs.

**Missing coverage.** A `childcall` contract case fixing the accepted window
shape per phase (frame plus queued unaddressed input, a foreign frame, and two
expected frames).

## L3. Interaction treats a Tool child's start failure and a Delegate child's start failure differently, undocumented

**Symbols.** `strategy/interaction/execution.go:498-506`;
`strategy/interaction/doc.go:23-27`; pinned by
`interaction/child_failure_test.go:50-60` and
`interaction/publication_delegate_test.go:44-46,73-77`.

**What is wrong.** For the same Host condition (child admission rejected), a
`childCallsTool` batch returns `agent.Fail(consumed, failure)` and terminates
the Interaction, while the Delegate path converts the failure into a
model-visible rejected result and continues. A budget or capacity rejection of a
Tool child therefore kills the Process with no chance for the model to react,
whereas the identical rejection of a Delegate child is recoverable. Both
behaviours are deliberate enough to be tested, but `doc.go` documents neither the
asymmetry nor its reason, and no test asserts both paths under one Host
condition.

**Fix layer.** `strategy/interaction`: align the two paths, or state the policy
and its reason in `doc.go`, with one test that fixes the documented difference.

## L4. `StageTopology` encodes limits that the Stage does not own, and the projection's JSON differs by marshaller

**Symbols.** `strategy/workflow/topology.go:68-73`;
`agent/quota.go:20-53` (`{"maximum":null}` means unlimited).

**What is wrong.** `omitempty` never omits a struct value, so every Transform,
Call or Switch Stage projects `"max_iterations":{"maximum":null}` — which reads
as "unlimited iterations" for a Stage that has no iteration limit — even though
the field comment says limits are non-zero only for the Stage kinds that own
them. `window_size`/`max_items` add a second problem: `encoding/json` omits the
zero `uint32`s but `encoding/json/v2` emits `0`, so the same projection has two
wire shapes depending on the Host's marshaller.

**Evidence.** Measured with both marshallers:
`{"id":"transform","max_iterations":{"maximum":null}}` (v1) and
`{"id":"transform","window_size":0,"max_items":0,"max_iterations":{"maximum":null}}`
(v2). Replacing the tag with `omitzero` omits the field under both marshallers
while keeping `{"maximum":3}` for a real Loop bound.
`strategy/workflow/topology_test.go:213-227` only re-decodes `max_items`.

**Fix layer.** `strategy/workflow/topology.go`, plus a projection-shape
assertion for a non-Loop Stage.

## L5. `planning` keeps an unreachable failure branch and its dead code

**Symbols.** `strategy/planning/definition.go:153-176` (`problem()`),
`strategy/planning/problem.go:22-40` (the only failure sources),
`strategy/planning/definition.go:63-75` (construction already excludes them),
`strategy/planning/execution.go:117,336` (`planning.problem.invalid`).

**What is wrong.** `NewProblem` fails only for an invalid Goal, an invalid
Action or a duplicate Action name; `NewDefinition` validates all three before
the Strategy can run, and `binding.Valid()` implies `action.Valid()`. So
`problem()` never returns an error: the `FailureKindContract` branch at
`execution.go:117` and the `planning.problem.invalid` code cannot be reached by
any input. A defensive branch no input can reach hides the fact that these are
construction invariants and misleads the next reader about the boundary.

**Fix layer.** `planning`: make `problem()` total (no error result), or state the
invariant at construction and delete the branch and the code.

## L6. `TestSwitchRunsOnlyTheSelectedManagedChild` is stronger than its assertion

**Symbols.** `strategy/workflow/switch_test.go:17,57-60`.

**What is wrong.** The test asserts only the final value. An implementation that
starts both case children and adopts only the selected result still passes,
although "runs only the selected managed child" is exactly the contract that
separates Switch from Fork. `loop_test.go:76-81` shows the in-repo pattern
(`CaptureTree` plus a tree-shape assertion).

**Fix layer.** The test: capture the tree and assert exactly one depth-1 child
whose `DeploymentRef` is the selected case.

## L7. The tree-durability conformance suite never exercises `TreeCheckpointKindProgress`

**Symbols.** `agenttest/tree_durability_crash_conformance.go:133-145`; producer
`agent/tree_runtime.go:1021-1035`; real user `strategy/interaction/execution.go`
(`agent.Checkpoint`).

**What is wrong.** `TreeCheckpointKindProgress` appears nowhere in `agenttest`,
so a Host that mishandles that cut (for example by treating every non-start
checkpoint as an append) passes the whole suite until a Strategy that returns
`agent.Checkpoint` runs in production.

**Fix layer.** `agenttest`: one fixture mode returning `Checkpoint` plus the
corresponding crash point.

## L8. `ScriptedDispatcher`'s fail-closed behaviour and its three sentinels are unprotected

**Symbols.** `agenttest/dispatcher.go:113-118,147,156-164`;
`agenttest/observation.go:12,76`; no test references
`ErrUnexpectedDispatch`, `ErrEffectMismatch` or `ErrInvalidEventPredicate`.

**What is wrong.** The fixture documents that it fails calls beyond or contrary
to its script "because a dispatcher that quietly answers anything turns an
ordering bug into a passing test", and no test makes it do so. Removing the
bound check and the effect-mismatch check keeps the entire workspace green, so a
fixture that silently answers any dispatch is indistinguishable from the correct
one. The three exported sentinels are never matched anywhere, so their
classification is untested too.

**Fix layer.** `agenttest` tests: overrun the script, mismatch `ExpectedEffect`,
and pass a nil predicate.

## L9. The durability conformance suite asserts through the private mailbox wire

**Symbols.** `agenttest/signal_durability_conformance.go:158-183` (anonymous
`mailbox.signals[].id/payload` struct), public owner
`agent/process_snapshot.go:98-101` (`SignalReceipts`),
`agent/signal_receipt.go:29-37` (`PendingSignal`, `Matches`), `agenttest/doc.go`
("exercises public … contracts without simulating private Engine state").

**What is wrong.** The suite decodes the snapshot's private persisted member
names instead of using `ProcessSnapshot.SignalReceipts()`, so a legitimate
rename of the Scope-owned mailbox wire breaks every third-party Host's
conformance run for a non-contract reason, and the suite pins a representation
it does not own. The same fact is already asserted through the public API in
`messaging/delivery_test.go:95-99`.

**Fix layer.** `agenttest`: assert admission with `SignalReceipts()` and
`PendingSignal()`/`Matches`.

## L10. `panicinfo.Capture` can return invalid UTF-8

**Symbols.** `agent/internal/panicinfo/diagnostic.go:19-21`
(`message[:MaxMessageBytes]`), contrast the kernel's diagnostic owner
`agent/diagnostic.go:23-35` (`ToValidUTF8` plus a rune-boundary retreat).

**What is wrong.** The fixed byte cut is not rune-aligned, so
`ListenerPanic.Message` (and anything a Host logs or exports from
`Engine.ObservationFailures`) can contain a truncated multi-byte sequence;
`panic(strings.Repeat("€", 2000))` splits a rune at byte 4096. The kernel already
owns a correct normalization rule for the same bound, and every ingress/egress
codec rejects invalid UTF-8, so the diagnostic path is the only place that can
produce it. Tests currently pin ASCII only.

**Fix layer.** `agent/internal/panicinfo`: use the same bounded/valid-UTF-8 rule,
or delegate to `agent.NormalizeDiagnostic`.

## L11. `messaging` mints a wire-visible Signal namespace from an inline literal

**Symbols.** `agent/messaging/dispatcher.go:82`; kernel vocabulary
`agent/identity.go:16,48,118-124`.

**What is wrong.** The kernel declares its reserved namespace
(`signal:engine:`), its tagged derivation rule, and the rule that an external
Signal must avoid that namespace. `messaging` derives a second, persisted
identity namespace from the undeclared literal `"signal:message:"`; that value
appears in the recipient's mailbox record and in `messaging.Receipt.SignalID`,
which senders persist for reconciliation, so an edit of the literal silently
changes every delivery identity. No test asserts the namespace or the
derivation.

**Fix layer.** `agent/messaging`: a named prefix constant with the reason it must
not change, plus a format assertion.

## L12. Admission re-serializes the whole tree, and no benchmark covers that path

**Symbols.** `agent/tree_runtime.go:2826-2860` (`validateSnapshotCapacity`:
`json.Marshal(treeSnapshotBase())` plus `snapshotAdmissionSize()` per member),
`agent/process_state.go:170,235,482` (a second candidate serialization per
admission), `agent/process_state.go:488-523`.

**What is wrong.** The tree byte quota is enforced by encoding the tree header
and every retained Process on *every* admission (signal delivery, child start,
step adoption), so admission cost grows with the retained tree size, and each
admission encodes the candidate twice. Nearby benchmarks measure the same
encoding work for capture (1.07 ms / 2.5 MB at 255 Processes), but the suite has
no benchmark for the admission path, so a regression there would be invisible.
This is an inspection signal, not a measured bottleneck: measure before changing
the representation (for example an incrementally maintained encoded size).

**Fix layer.** Add an admission benchmark (delivery and child start at several
tree sizes); only then decide whether the check moves to commit boundaries or
keeps a running size.

## L13. The agent package is imported two ways, with no gate

**Symbols.** 164 of 204 files that import the kernel alias it as
`agent "github.com/Tangerg/scope/agent"` (a redundant self-alias); `agenttest`,
`messaging`, `collaboration`, `planning`, `workflow`, `coordination`,
`internal/` and `examples/` mix both styles, sometimes inside one package
(`interaction` 62/72, `planning` 19/26, `workflow` 24/27, `coordination` 13/15).
`.golangci.yml` disables `staticcheck`'s `ST1003` and defines no rule for it.

**What is wrong.** One repository, one convention: project rules require one
canonical choice per concern and mechanical enforcement rather than prose.
Today neither style is a convention, and a new file can add either.

**Fix layer.** Pick one form (the unaliased import is idiomatic Go and already
used by `agenttest`), migrate, and add a guard so the mix cannot return.

## L14. Recovery validation has no vocabulary for which domain fact was contradicted

**Symbols.** Every recovery-path validator whose condition joins independent
invariants and whose body returns one error, worst first:

| Clauses | Site | What the Host is told |
|---|---|---|
| 10 | `agent/tree_snapshot.go:367` `validateChildStart` | `ErrInvalidChildStart` — a bare sentinel, no message at all |
| 9 | `agent/process_snapshot.go:336` `validateContract` | `"incomplete Process identity or state"` (also recorded under M1) |
| 7 | `agent/effect_phase.go:157` `resolveUnknown` | `"effect is not settled as unknown or resolution does not match"` |
| 7 | `agent/mailbox.go:471` `restore` | `"invalid Signal record"` |
| 5 | `agent/effect_phase.go:87` `validatePhase` | `"prepared Effect phase and settlement disagree"` |
| 5 | `agent/effect_phase.go:114` `settle` | `"effect is not pending or settlement does not match"` |
| 5 | `agent/prepared_step.go:82` `validate` | `"invalid prepared Step boundary"` |

**What is wrong.** `validateChildStart` checks ten independent facts — the child
is absent, its parent identity disagrees, its child key disagrees, its
`DeploymentRef` disagrees, its `Budget` disagrees, its capability set disagrees,
or its request digest is missing or disagrees — and reports all of them as
`ErrInvalidChildStart` with no detail. `agent/doc.go` promises that parsing
"validates the structure and the recorded domain facts", and it does; what it
cannot do is say which fact lost. Recovery is this module's reason to exist, so
the one path a Host cannot diagnose is the one that decides whether a tree comes
back.

This is the same reasoning the module already applies to failures:
`NewFailure`'s contract requires a stable code beside the message because
"matching on message text is what makes error handling break on wording changes"
(`agent/failure.go:49-53`). Snapshot rejection has neither a code nor, in the
worst case, a message.

The condition count is not the defect — a `Valid() bool` method is allowed to be
a single conjunction, and `tree_durability.go:86,286` and `child_wait.go:348`
are correct as written. The defect is a validator that owns an `error` return
and does not use it.

**Fix layer.** The validators. One error per contradicted fact, each naming the
fact, keeping `ErrInvalidSnapshot` / `ErrInvalidChildStart` as the wrapped
sentinel so existing `errors.Is` matching is unaffected. This is mechanical and
behaviour-preserving for every accepted snapshot.

**Missing coverage.** `tree_snapshot_test.go` drives table cases through
`ParseTreeSnapshot` and asserts on the sentinel, so splitting the messages needs
those tables extended with the expected detail per case — which is also what
would have exposed the missing detail.

## Corrections to earlier entries

- The earlier `TestProcessConstructionRemainsEngineOwned` entry
  (`agent/architecture_test.go:127`) is **withdrawn**: the guard enumerates the
  kernel package's own directory, and a subdirectory is a different Go package
  that cannot construct a `Process` (its fields are unexported to package
  `agent`). The sibling guard recurses because it must cover test files in every
  package; the difference is deliberate, not an inconsistency.
- The earlier "two import styles" entry is retained as L13 but reclassified: a
  standard violation, not a defect, and the fix includes the missing gate.
- The earlier positional-decoding entry and the earlier planning-quota entry are
  retained as H1 and M2 with new reproduced evidence and a root-cause statement.
  The earlier "single-child handshake window check is duplicated" entry is
  retained as L2.

## Checked and rejected (do not "fix" these)

- **Interaction streaming budget (`strategy/interaction/dispatcher.go:181-256`).**
  The delta accounting (per-delta framing counted against `MaxResponseBytes`) and
  the final-envelope check are deliberately different rules: the config contract
  states "stream framing counts toward the cumulative limit", and
  `ErrModelResponseTooLarge` documents that after the model starts the Effect
  remains unknown and no partial response is promoted. Measured numbers (a
  one-character text delta encodes to 80 bytes, the same content's aggregate
  envelope to 158 bytes, and a minimal `{"operation":"model_call"}` base is 26
  bytes) show both rules are enforced on what they claim to bound, so there is no
  bound violation — only a sharp edge the Host should read the doc for. Changing
  it is a product decision, not a repair.
- **`coordination.Timer.ReplayPolicy` "untested" (`timer.go:56-60`).**
  `strategy/coordination/deadline_test.go:27-70` restores a captured pending
  timer and requires two dispatches with one identity, which fails if the
  `ReplayPolicySameIdentity` branch is removed. The malformed-payload test is
  additional coverage, not the only coverage.
- **`ParseProcessSnapshot`'s required-member list
  (`agent/process_snapshot.go:47-56`, `Limits.UnmarshalJSON`,
  `TreeLimits.UnmarshalJSON`).** `jsonwire.Decode`'s variadic arguments name
  members that must be present and non-null; they are strictness checks that
  reject a flat or incomplete superseded shape, not a compatibility shim.
- **`snapshotAdmissionSize` reserving local wait settlements on a wire copy.**
  It reserves the representation of a settlement that is known and will exist,
  without mutating owner state; no partial mutation is observable.

## Suspected, not proven (evidence still missing)

- **Zero-progress `Continue` in `collaboration`
  (`execution.go:219-227`, `state.go:222`).** A `Continue(0)` with no Effects is
  legal for the kernel and re-enqueues the Process, so an empty window in
  `phaseApplying` would spin until `Budget.Steps` is exhausted (or forever with
  unlimited Steps). The suspected trigger is a Decision carrying only Controls
  while the window is empty; the shared scheduling rules make an empty window
  unlikely, and no test produced it. Missing evidence: a Step-level case calling
  the Execution with an empty window in `phaseApplying` and observing the
  Transition.
- **Unchecked member lookups in child-wait reporting
  (`agent/tree_runtime.go:2530-2566`).** `childWaitOutcomes` and
  `subtreeUnresolvedEffects` dereference `t.processes[childID]` without a nil
  check. No reachable path was found: registrations are created only after a
  child start settles, and a settled child is removed only together with a tree
  fault, after which `notifyChildWaits` returns early. If a future change lets a
  registration outlive its child, this becomes a nil panic instead of a diagnosed
  error; the invariant deserves a test or an explicit statement.

## Verification notes

- `scripts/check.sh` runs `build vet test tidy pinned-test lint vuln`; it does
  not run benchmarks, which is why M1 survives a green gate. Every other entry is
  reachable through the passing tests listed above.
- The reproductions in H1, M2 and L1 were written and run outside the repository
  (a scratch module with `replace` directives in `/tmp`) using only exported
  kernel API and the affected strategy packages.
- The audit read the kernel root package, the
  `strategy/{workflow,planning,coordination,collaboration,interaction,internal}`
  production code, `agenttest`, `messaging`, `internal/*` and `examples/*`, plus
  the design and verification documents named above. Provider adapters, `a2a`,
  `mcp`, `otel` and `eval` were not audited.
- Earlier draft entries that did not survive verification were deleted and are
  recorded under *Corrections*; no entry in this document is retained on trust.
