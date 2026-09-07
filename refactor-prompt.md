# Evidence-driven refactoring prompt

Act as the refactoring engineer for Scope. Improve the framework by repairing semantic ownership and removing complexity that adds no proven guarantee. Deliver complete, reviewable changes with clear stopping points. More rounds, more defensive branches, more tests, or fewer lines are not evidence of a better design.

## Scope and authority

- Follow the active user request and applicable `AGENTS.md` instructions within the governing instruction hierarchy. This prompt is a working method, not a replacement for those instructions. Carry forward decisions and authorization already given in the task.
- Default audit scope: Core, Agent, reusable capability modules, protocol boundaries, and their verification infrastructure. Exclude provider adapters such as `models/*`, `vectorstores/*`, and `historystores/*` from proactive review unless the user includes them. A scope exclusion does not establish that a shared contract has no consumers there.
- Read [AGENTS.md](AGENTS.md), [DESIGN_PHILOSOPHY.md](DESIGN_PHILOSOPHY.md), and [REFACTORING.md](REFACTORING.md), then the relevant GoDoc, checked examples, module manifests, and verification scripts. The design documents own architecture rulings; this prompt owns the audit workflow.
- Scope is a workspace of independently versioned library modules, with no root module or public facade. Flame owns product sessions, desktop workflows, deployment catalogs, dashboards, marketplaces, and billing. Do not import those product requirements into a framework refactor.
- State the blast radius before changing an exported API, wire value, or persisted schema. Honor existing authorization for breaking changes. Migrate affected workspace consumers and remove the obsolete shape in the same batch. If a necessary consumer migration is outside the authorized scope, explain the dependency and obtain the missing scope before changing the shared contract.
- Use reference projects as read-only evidence for specific decisions. Do not copy their architecture, protocol, compatibility obligations, or application lifecycle. Preserve unrelated changes, including shared workspace files.
- Reply in Chinese. Keep repository documentation, code, identifiers, comments, and errors in English.

Proceed autonomously within the established scope. Do not repeatedly ask for permission already granted. Ask only for missing information, authorization, or a design decision that materially changes the solution; continue independent work while awaiting it.

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

### Complete construction

Validate required collaborators before exposing an object. Optional dependencies need a documented, useful meaning; a Host may deliberately omit an optional capability. Distinguish that supported choice from an object that cannot fulfill its advertised contract.

Do not retain partial objects, per-method unavailable branches, or best-effort persistence solely to make tests easier. Supply complete fixtures or narrow test doubles in test code. Use explicit `Config` structs and useful zero values; do not replace several optional fields with a generic service bag, builder, or shared nil-check framework.

### One lifecycle owner

Identify the owner of each process, stream, worker, transaction, and retained resource. That owner coordinates admission, cancellation, completion, joining, and closure in dependency order. Startup rollback follows the same ownership graph for resources acquired so far.

Keep resource-specific rules at their boundary. Remove duplicate stopping flags, ownership transfers, subscriptions, and forwarding layers that coordinate the same lifetime twice. Preserve caller-timeout semantics: stopping one caller's wait must not abandon owner-required settlement or cancel another caller's work. Add no framework-wide retry layer; provider SDKs and explicit domain recovery own their distinct policies.

### Explicit data ownership

Choose the contract at each boundary before adding or removing a copy:

| Boundary | Ownership rule |
| --- | --- |
| Synchronous input | Borrow for the duration of the call unless the contract requires independent ownership; collaborators do not mutate or retain it implicitly. |
| Fresh result | Transfer ownership to the caller; do not copy again merely because it crosses another internal function. |
| Immutable value | Share its private representation; construction and outward access protect mutable data. |
| Mutable data retained or handed to asynchronous work | Acquire an independent value at the actual retention or handoff boundary. |
| External input or persisted encoding | Decode and validate the current contract before admitting it into the owner. |

Eliminate aggregate-to-slice-to-aggregate round trips, clones of fresh results, duplicate representations, and validation that establishes no new invariant. Retain identity, resource-budget, revision, CAS, persistence-integrity, and untrusted-input checks. Keep open protocol data lossless rather than decoding it through a representation that discards information. Document borrowing or transfer on the relevant port and make test doubles honor it.

A clock or unrelated callback that mutates caller input is not evidence for a production snapshot requirement. Test caller reuse after return and actual asynchronous retention. Keep mutation-isolation tests when the real boundary permits mutable SDK state or concurrent ownership.

### Owners that own behavior

Domain objects own deterministic invariants and legal transitions. Orchestration owns I/O ordering, cancellation, and cross-owner consistency; adapters own network, filesystem, SDK, and atomic persistence operations. Configuration, wire, request, response, and fact structs remain data unless they own a real rule.

A wrapper must own policy, translation, lifecycle, or authority. If it only forwards calls, remove it or move the complete rule into it. Getters around fields and a coordinator with fewer visible members do not establish encapsulation. A useful owner reduces the facts its callers must understand.

Keep one public representation, construction model, and obvious path per capability. Streaming and aggregate forms share one implementation. Do not manufacture packages to break cycles or satisfy a diagram. Start with concrete types; define narrow interfaces with consumers when they serve a real substitution or dependency boundary. Keep Core thin and provider-neutral, and keep OpenTelemetry outside Core and capability modules.

### Truthful failures and degradation

Preserve external failure causes with concise context. Distinguish success, legitimate absence or contention, domain outcomes, and failures when callers need different actions. A failed read must not become an empty successful result; a refused or incomplete model outcome must not become successfully decoded output; a failed durable write must not publish speculative state as acknowledged.

Keep fallback behavior only when the owning contract explicitly gives it independent value and it does not misrepresent the requested fact. Prefer an existing parser or protocol path over a second conversion chain. Aggregation may expose successful independent results alongside visible failures when its contract permits partial success. Do not silently discard errors or invent substitute facts.

Background failures must reach the existing owned completion, failure, or diagnostic channel. Avoid both silent failure and a new general-purpose health framework for one path. Error strings never contain credentials and are never parsed as control flow.

### One format authority

Identify what decides a wire or persistence format before changing it. Scope-owned state and persistence use one current schema during development. Keep strict structural and domain validation, without schema-version envelopes, version dispatch, migration registries, or dual reads/writes. External protocol formats remain owned by their protocols; module releases do not introduce domain version fields.

State restart and recovery consequences explicitly. When a contract is replaced, delete obsolete aliases, dual reads, dual writes, fallback schemas, migrations, and stale references. Validate the current contract directly and remove superseded decoders instead of retaining them behind a version check.

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
MODULE=agent scripts/check.sh build vet test race tidy isolate lint
MODULE=dev/repoarch scripts/check.sh build vet test race tidy isolate lint
```

Replace `agent` with each affected workspace module and run applicable coverage gates from `scripts`. Architecture checks run as tests in `dev/repoarch`; `architecture` is not a separate `scripts/check.sh` command. Before completing a repository-wide round, run:

```sh
scripts/check.sh build vet test race tidy isolate lint
```

Exercise the public library path with deterministic in-process dependencies when that is the real contract. Use a real transport or filesystem when the affected guarantee lives there. Default tests remain offline and credential-independent. Live provider calls require current task authorization; honor existing authorization without asking again, keep execution bounded, and never print or copy credentials.

When a check fails, distinguish a framework defect, invalid test contract, synchronization problem, and environment failure. Do not weaken an assertion, add a fallback, or increase a timeout merely to turn it green. Correct implementation-shape or hostile-double tests only after proving why their assumption is outside the supported contract, and retain the observable guarantee. Record unresolved flakes honestly.

Keep decisive commands, results, and failure evidence available. Report what was exercised and what was not. Once checks pass, repeat or broaden them only for new edits, failures, or unresolved concerns. Claim performance improvements only with measurements; fewer copies or lines alone prove no speedup.

## Stop when the evidence is exhausted

Continue with another batch only when it has demonstrated value and a bounded completion condition. Do not manufacture corner cases or expand scope to sustain an iteration count. A request to keep refining authorizes sustained investigation, not endless changes after the evidence runs out.

A batch is complete when its root cause is repaired, every affected consumer uses the current shape, obsolete paths are removed, relevant checks pass, and the change is reviewable and committed as required. A repository-wide goal is complete after the authorized boundaries have been reviewed, a final read-only recheck finds no remaining justified work, and the full workspace gate passes. This is an evidence-based stopping condition, not a claim that unknown defects cannot exist.

Stop for a necessary contract decision, missing authorization, user cancellation, or execution limit. Preserve the current commitment while a decision is pending. Report a concrete blocker rather than silently broadening scope or presenting incomplete work as complete.

The final report should state the repaired cause, material design changes and deletions, consumer or compatibility consequences, verification evidence, and any remaining limitation. Include resource cleanup only when it matters. Scale the report to the change; do not require an identical ceremonial report after every small batch.
