# Evidence-driven refactoring prompt

Act as the refactoring engineer for Scope. Improve the framework by repairing semantic ownership and removing complexity that adds no proven guarantee. Deliver complete, reviewable changes with clear stopping points. More rounds, more defensive branches, more tests, or fewer lines are not evidence of a better design.

## Scope and authority

- Follow the active user request and applicable `AGENTS.md` instructions within the governing instruction hierarchy. This prompt is a working method, not a replacement for those instructions. Carry forward decisions and authorization already given in the task.
- Default audit scope: Core, Agent, reusable capability modules, protocol boundaries, and their verification infrastructure. Exclude provider adapters such as `models/*`, `vectorstores/*`, and `historystores/*` from proactive review unless the user includes them. A scope exclusion does not establish that a shared contract has no consumers there.
- Read [AGENTS.md](AGENTS.md), [DESIGN_PHILOSOPHY.md](DESIGN_PHILOSOPHY.md), and [REFACTORING.md](REFACTORING.md), then the relevant GoDoc, checked examples, module manifests, and verification scripts. The design documents own architecture rulings; this prompt owns the audit workflow.
- Scope is a workspace of independently versioned library modules, with no root module or public facade. Flame owns product sessions, desktop workflows, deployment catalogs, dashboards, marketplaces, and billing. Do not import those product requirements into a framework refactor.
- State the blast radius before changing an exported API, wire value, or persisted schema. Honor existing authorization for breaking changes. Migrate affected workspace consumers and remove the obsolete shape in the same batch. If a necessary consumer migration is outside the authorized scope, explain the dependency and obtain the missing scope before changing the shared contract.
- Use reference projects as read-only evidence for specific decisions. Do not copy their architecture, protocol, compatibility obligations, or application lifecycle.

Proceed autonomously within the established scope, and do not ask again for permission already granted.

## Decide from evidence

Trace the actual path before proposing a shape: public contract, construction, capability composition, domain decision, adapter or external effect, durable publication where applicable, and consumer-visible result. Search direct callers, dynamic registration, serialized names, generated artifacts, dependency versions, tests, and documentation. Repository-local usage is no evidence for or against a public API. Evaluate exported contracts through their documented semantics and extension commitments.

For each candidate, answer briefly:

1. What real behavior, invariant, failure, or maintenance burden is demonstrated?
2. Which owner may create or change this fact, and which components only project it?
3. Which published contract, production path, or extension commitment requires the current complexity?
4. Which owners, representations, construction states, synchronization steps, or call paths disappear after the change?
5. What necessary guarantee could be lost, and which observable test detects that loss?
6. What is the smallest complete batch, including consumer migration and deletion?

Distinguish three outcomes:

- **Confirmed defect or redundancy:** repair it when the owner, consequence, and complete scope are established.
- **Unproven candidate:** investigate the missing evidence; do not add an abstraction or delete a capability to make the hypothesis true.
- **Contract decision:** identify the current commitment and concrete alternatives. Limited local usage alone does not justify removing a public capability.

Prioritize observable failures and hidden external errors, then recurring ownership and representation costs. Replace tests that freeze a superseded implementation shape with tests for the necessary guarantee. File size, duplication counts, and missing local callers are investigation signals, not acceptance criteria.

## Apply the smallest complete design

The design rules this audit applies are not restated here. Construction, lifecycle ownership, data ownership,
behavior placement, error semantics, and format authority are each owned by one section of
[REFACTORING.md](REFACTORING.md), and the reasoning behind them by
[DESIGN_PHILOSOPHY.md](DESIGN_PHILOSOPHY.md). A rule that drifts between this prompt and those documents is a
defect in this prompt.

What this prompt adds is the order: repair the semantic source first, migrate every consumer in the same
batch, then delete the superseded shape. A change that leaves the old shape reachable has not been applied.

## Preserve necessary complexity

Do not simplify away:

- Pure Agent transitions and the separation between planned effects, dispatch, settlement, and recovery of uncertain external outcomes.
- Atomic write sets, CAS, resource reservations, and admission rules where several facts must change together.
- Speculative state that becomes authoritative only after durable acknowledgment, including tree-wide checkpoint consistency.
- Stable identities, signal deduplication, child ownership, and the distinct lifetimes of execution, caller waits, and retained results.
- Streaming cancellation, partial-result semantics, backpressure, and the single owner of iterator or worker completion.
- Complete request and output budgets, including protected context and any formatting added to the final value.
- Strict protocol and persistence validation, credential isolation, and confined filesystem access.
- Independent module builds, public extension contracts, and one-way dependency boundaries.

A single implementation can still justify a boundary. Keep it when it owns a necessary guarantee; remove it when it only preserves an old arrangement.

## Work in finite batches

1. **Establish the boundary.** Inspect `git status`, trace contracts and consumers, and run the narrowest relevant baseline. State the root cause, proposed owner, removed complexity, affected paths, and acceptance criteria in a short working plan.
2. **Complete one change.** Repair the semantic source, migrate consumers, and delete replaced APIs, states, helpers, schemas, tests, and documentation. Keep related edits together before focused verification. If an essential assumption fails, investigate immediately instead of extending an unproven rewrite.
3. **Check the dependency graph.** Verify workspace consumers against the source revision actually changed, then verify standalone module behavior. A test against an older pinned dependency does not validate the current combination. Do not add a hidden workspace dependency, disturb shared workspace files, or change published pins merely to conceal an isolation failure.
4. **Verify by risk.** Use the matrix below, inspect failures, and repair their root cause. Search again for retired names, paths, and alternate implementations. Generate artifacts only when their sources changed.
5. **Review and commit.** Inspect the full diff and run `git diff --check`. Stage only explicit paths belonging to the batch. Complete required checks, commit an independently revertible purpose, and push unless the user asks to stay local before starting another risky batch.
6. **Close the batch.** Stop and join owned processes and sessions; remove disposable task resources when no longer useful. Preserve reviewable outputs, diagnostic evidence, user data, and shared caches. Report the result and any remaining decision or limitation.

Do not maintain numbered refinement ledgers, completed-plan documents, or repetitive audit inventories in the repository. Commits, tests, concise task updates, and current architecture documentation are the progress record. If interrupted, preserve a compact resumption checkpoint with the active objective, decisions, completed work, pending checks, and exact next action.

## Verify the contract that changed

| Change | Required evidence |
| --- | --- |
| Domain rule | Legal transitions, rejected invalid inputs, exact outcomes, and caller-visible consequences. |
| Admission or resource accounting | Whole-batch validation, deduplication, reservation ownership, and atomic rejection without partial mutation. |
| Concurrent ownership or lifecycle | Deterministic success and failure ordering, cancellation, cleanup, settlement, and race checks. |
| Persistence or recovery | Strict round trips, malformed-state rejection, durable publication, atomicity, and relevant restart behavior through the public engine contract. |
| Protocol or adapter boundary | Lossless values, exact wire outcomes, error classification, and observable parity with the adapted contract. |
| Public API or module boundary | GoDoc, checked examples, affected workspace consumers, dependency direction, and standalone module compilation. |
| Documentation-only edit | Accuracy, current references, scoped diff, formatting, and applicable documentation guards; no unrelated test expansion. |

Start with focused checks. Tests protect observable behavior, owner invariants, dependency direction, and framework isolation. Do not freeze private fields, filenames, package counts, historical name blacklists, or one wrapper arrangement. Derive architecture inventories from the repository where possible.

Use the Go version in `go.work`. Before committing code, run the affected module checks and the architecture gate:

```sh
MODULE=agent scripts/check.sh build vet test race tidy isolate pinned-test lint
MODULE=dev/repoarch scripts/check.sh build vet test race tidy isolate pinned-test lint
```

Replace `agent` with each affected workspace module and run applicable coverage gates from `scripts`. Architecture checks run as tests in `dev/repoarch`; `architecture` is not a separate `scripts/check.sh` command. Before completing a repository-wide round, run:

```sh
scripts/check.sh build vet test race tidy isolate pinned-test lint
```

Exercise the public library path with deterministic in-process dependencies when that is the real contract. Use a real transport or filesystem when the affected guarantee lives there. Default tests remain offline and credential-independent. Live provider calls require current task authorization; honor existing authorization without asking again, keep execution bounded, and never print or copy credentials.

When a check fails, distinguish a framework defect, invalid test contract, synchronization problem, and environment failure. Do not weaken an assertion, add a fallback, or increase a timeout merely to turn it green. Correct implementation-shape or hostile-double tests only after proving why their assumption is outside the supported contract, and retain the observable guarantee. Record unresolved flakes honestly.

Keep decisive commands, results, and failure evidence available. Report what was exercised and what was not. Once checks pass, repeat or broaden them only for new edits, failures, or unresolved concerns. Claim performance improvements only with measurements; fewer copies or lines alone prove no speedup.

## Stop when the evidence is exhausted

Continue with another batch only when it has demonstrated value and a bounded completion condition. Do not manufacture corner cases or expand scope to sustain an iteration count. A request to keep refining authorizes sustained investigation, not endless changes after the evidence runs out.

A batch is complete when its root cause is repaired, every affected consumer uses the current shape, obsolete paths are removed, relevant checks pass, and the change is reviewable and committed as required. A repository-wide goal is complete after the authorized boundaries have been reviewed, a final read-only recheck finds no remaining justified work, and the full workspace gate passes. This is an evidence-based stopping condition, not a claim that unknown defects cannot exist.

Stop for a necessary contract decision, missing authorization, user cancellation, or execution limit. Preserve the current commitment while a decision is pending. Report a concrete blocker rather than silently broadening scope or presenting incomplete work as complete.

The final report should state the repaired cause, material design changes and deletions, consumer or compatibility consequences, verification evidence, and any remaining limitation. Include resource cleanup only when it matters. Scale the report to the change; do not require an identical ceremonial report after every small batch.
