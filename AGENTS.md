# AGENTS.md

## Priorities

Preserve correctness, security, data integrity, and explicit requirements. Within those constraints, optimize
for maintainability, readability, and testability. Add extensibility, flexibility, and reuse only when current
needs justify them.

Use design principles as judgment aids, not a checklist of patterns to implement. Resolve trade-offs in favor
of clear behavior and lower overall complexity.

## Working approach

- Read applicable instructions and relevant implementation, callers, and tests. Expand context as dependencies
  or uncertainty require; load documentation and skills only when their scope matches the task.
- Use commands verified in repository scripts, configuration, or CI. Follow sound local conventions; introduce
  a different pattern to address a concrete limitation, not a stylistic preference.
- For cross-cutting or risky work, identify intended behavior, affected contracts, and verification before
  editing. Make straightforward changes directly.
- Resolve ambiguity from contracts and repository evidence. Ask only when remaining uncertainty materially
  affects behavior, scope, or data safety; otherwise use the simplest consistent interpretation.
- Within the authorized scope, implement and verify the change. Run checks and fix introduced failures without
  repeated approval in confirmed isolated environments. Before unfamiliar or potentially state-changing
  commands, confirm the target environment and expected side effects are within the authorized scope. Changes
  to shared or external state require explicit authorization; a command named `test` is not proof of isolation.
- Preserve unrelated work. Production actions, destructive data operations, and destructive Git operations
  require explicit authorization beyond permission to edit code.

## Design and implementation

### Simplicity and abstraction

- **Occam's razor / KISS:** Choose the least complex sufficient solution: fewer assumptions, concepts, states,
  dependencies, and indirections. Reduce understanding and change costs, not line count.
- **YAGNI:** Add only capabilities required now. Do not prebuild configuration, extension points, or
  frameworks. Necessary safety checks and tests are not speculative work.
- **DRY:** Give each business rule one authoritative representation. Share stable knowledge, not merely
  similar syntax; keep independently changing concepts separate.
- An abstraction must reduce complexity for its callers, consolidate stable knowledge, or isolate an actual
  variation. Moving code behind another name is not enough.
- Prefer standard-library and existing project capabilities. Add dependencies only when their benefits justify
  their maintenance cost; use established implementations for security-sensitive primitives.

### Boundaries and contracts (SOLID)

- **SRP:** Group code by its reason to change; split independent responsibilities, not cohesive logic to
  satisfy arbitrary size limits.
- **OCP:** Extend behavior at demonstrated variation points; repair flawed abstractions instead of preserving
  them behind extra layers.
- **LSP:** Preserve behavioral contracts, including invariants and failure semantics. Do not strengthen
  preconditions or weaken postconditions.
- **ISP:** Shape small, cohesive interfaces around consumer needs, not every capability of an implementation.
- **DIP:** Separate business policy from volatile infrastructure through explicit boundaries; do not create an
  interface for every type.
- **LoD:** Depend on direct collaborators' public contracts, not their internal object graphs. Avoid
  forwarding layers that merely disguise coupling.

### Readability and state (Zen of Python)

- Use the host language's idioms. Prefer explicit dependencies, flat control flow, readable spacing, and
  coherent namespaces over implicit magic or clever compression.
- Represent necessary complexity behind clear boundaries. Keep justified exceptions local and prefer practical
  clarity over rigid uniformity.
- Keep mutable state minimal, ownership explicit, and each fact authoritative in one place. Separate business
  decisions from external I/O.
- Prefer one clear path per behavior. Simplify hard-to-explain logic without fragmenting cohesive code into
  tiny helpers.
- Make failures explicit; suppress only specific expected errors allowed by the contract. Never turn
  unexpected failure into apparent success.

### Data and performance (Rob Pike)

1. Do not guess bottlenecks or add speculative speed hacks.
2. Measure representative workloads before tuning; optimize significant bottlenecks and compare results
   against the baseline.
3. Choose algorithms for actual input sizes; consider constant costs as well as asymptotic complexity.
4. Use simple algorithms and data structures unless requirements or measurements justify the added complexity.
5. Design data representations and invariants first; simplify algorithms through better structure.

Respect known scale and resource limits during design.

### Comments

- Default to no comments; use naming, types, and structure to express intent.
- At critical data structures, non-obvious algorithms, interfaces, or pitfalls, explain **why**: constraints,
  trade-offs, or essential contracts the types cannot express, such as ownership, lifetime, concurrency, or
  failure semantics. Do not narrate operations or repeat signatures.
- Keep necessary comments accurate; remove stale comments and commented-out code. Preserve licenses and tool
  directives. Do not use TODOs in place of required work.

## Fixes and evolution

- Establish the root cause through reproduction, tests, or traced behavior. Fix the responsible model,
  invariant, or boundary and check other affected paths.
- Do not conceal defects with stacked special cases, duplicated state, blind retries, or silent fallbacks.
  Keep validation and resilience where real contracts require them.
- **Breaking changes are allowed** to fix faulty contracts or achieve a simplification worth the migration
  cost. Honor explicit compatibility requirements; do not break sound contracts for style.
- Update affected callers, types, tests, and documentation together. Address protocol, persisted-data, and
  external-consumer migrations explicitly; disclose what remains outside the task's control.
- Keep compatibility adapters only for real consumers or rollout needs, with a removal condition. Delete
  superseded code and configuration when that condition is met.
- Make the smallest complete change that fixes the cause. Refactor obstructive related code in verifiable
  steps; distinguish behavior-preserving cleanup from intentional contract changes.
- At iteration or milestone reviews, revisit repeatedly broken, frequently changed, or hard-to-test modules.
  Record out-of-scope debt with its impact and a trigger for revisiting it; do not start unrelated rewrites.

## Verification and completion

- Use checks sufficient to demonstrate changed behavior. Broaden coverage for shared contracts, cross-module
  changes, or build configuration. Honor required repository checks.
- For bug fixes, add regression coverage that exposes the original failure when feasible. Test observable
  behavior and contracts, including relevant boundaries and failures.
- Control time, randomness, and external state where needed for reliable tests. Do not distort production
  interfaces merely to mock them.
- Do not disable checks, skip failing tests, or weaken valid assertions to manufacture a pass. Correct test
  expectations only for intentional contract changes or demonstrated test errors.
- Review the diff for concrete defects, contract violations, and maintainability problems. Remove accidental
  edits, debug residue, and dead code; do not treat stylistic alternatives as defects.
- Finish when requested behavior and affected integrations are complete and relevant verification passes.
  Report genuine blockers rather than claiming completion; stop improving when acceptance criteria are met.
- Report changes, actual verification results, and any migrations or remaining risks. Distinguish passed,
  failed, and not-run checks; identify unrelated pre-existing failures.

## Maintaining these instructions

- Keep durable rules that prevent recurring mistakes or record non-obvious project decisions. Put scoped rules
  near affected code and occasional procedures in narrowly triggered skills or linked documentation.
- When maintaining instructions, remove stale or redundant guidance and evaluate changes on representative
  tasks. Let observed task outcomes guide further revisions. Keep skill descriptions short and triggers
  precise; do not rewrite policy during unrelated coding work.
- Enforce mechanical requirements through formatters, linters, hooks, and CI rather than repeated prose. Never
  weaken instructions or checks to excuse a noncompliant change.
## Project-specific rules

Rules that apply only to this repository live in [`PROJECT_RULES.md`](PROJECT_RULES.md).

@./PROJECT_RULES.md
