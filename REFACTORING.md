# Scope refactoring guide

This reference turns [`DESIGN_PHILOSOPHY.md`](DESIGN_PHILOSOPHY.md) into an editing method for every Scope module. [`AGENTS.md`](AGENTS.md) contains the short repository rules. Use this guide to decide what to change, what to leave alone, how to divide the work, and how to prove the result.

## Refactor toward the owner

A refactor improves structure while preserving the intended contract, or deliberately replaces an approved wrong contract. It is not a rewrite for stylistic uniformity and not a patch around a bad abstraction.

Fix the cause in the layer that owns it. If a software development kit (SDK) conversion creates an invalid value, repair the conversion or contract rather than coercing the value in every consumer. If a schema permits an impossible state, repair the schema rather than adding defensive branches throughout the runtime.

Do not retain a known-wrong name, field, type, wire shape, or call path to reduce the number of edits. Once a breaking change is approved, migrate every workspace consumer, example, test, and document in the same batch, then delete the old path.

## Start with evidence

Audit before editing:

1. Read the owning package's GoDoc, checked examples, callers, and relevant contract tests.
2. Search for every constructor, method, field, serialized name, interface implementation, example, and documentation reference in the blast radius.
3. Classify the finding by ownership: naming, misplaced behavior, duplicate API, dependency direction, lifecycle, error semantics, data representation, performance, or documentation drift.
4. Identify the source of truth and the symptom sites. Plan to change the source first.
5. Separate independent findings into independently revertible batches.
6. State the scope, alternatives, and blast radius before a structural or exported breaking change.

Repository-local call counts do not decide whether a public framework application programming interface (API) belongs. A locally unused export may be a valid downstream extension point; a heavily used export may still be the wrong abstraction. Judge responsibility, semantic ownership, and downstream utility.

## Choose the right refactoring scope

Use two maintenance rhythms:

- Small refactoring follows a few feature rounds. Inspect recently changed files for naming drift, duplicate truth, dead comments, misplaced behavior, excessive nesting, and new exports that bypass an existing owner.
- Large refactoring follows roughly ten to twenty feature rounds or a clear architectural signal. Inspect module boundaries, dead packages, god types, cross-package duplication, concrete dependencies crossing abstraction boundaries, and package or module splits.

Do not expand a small pass into an unbounded repository rewrite. Do not reduce a large boundary correction to scattered local patches. The ownership boundary determines the batch size.

## Make names carry semantics

Every noun names one concept and lifecycle across code, comments, errors, tests, and documentation. Rename a symbol when its data or behavior no longer matches its name.

Apply these naming rules:

- Use a plain verb for an operation. Reserve a past participle such as `Merged` or `Processed` for persisted state or a returned fact.
- Keep a field name aligned with its serialization tag. When they disagree, rename the field and migrate the wire contract if required instead of preserving two vocabularies.
- Remove package stutter such as `history.HistoryStore` when `history.Store` states the same meaning.
- Name a file for its contents. Replace `impl.go`, `interface.go`, `util.go`, and `helper.go` with responsibility names.
- Avoid empty Java-style roles such as `Impl`, `Service`, `Manager`, or generic `Handler` when the type has a more precise domain name.
- Use the lowercase initial of the receiver type for every method receiver.
- Do not let a parameter shadow an imported package. Resolve the package's declared name rather than guessing from the import path.
- Keep an importable single-capability package singular or uncountable, such as `tool`, `history`, or `web`. Use a plural only for a non-importable namespace of sibling implementations.
- Keep protocol and industry proper names in their established form when they express an objective integration fact.

Run `dev/repoarch` after naming changes; its gates cover receiver spelling, imported package shadowing, retired layouts, and repository identity.

## Put behavior on the domain owner

Prefer object-oriented, behavior-rich domain models over procedural code around anemic data bags. In Go, this means methods on the type that owns the semantics, not inheritance.

Move invariants, validation, derived values, classification, and pure state transitions onto the entity or value object they describe. A package-level function that repeatedly inspects one type or enforces its policy usually belongs on that type even when the function does not read stored fields.

Keep data models as data. Configuration, request and response data transfer objects, wire values, plain parameters, and fact records do not become rich merely by acquiring methods. Add behavior only when the type owns a rule.

Keep input/output (I/O) in adapters. Database transactions, atomic updates, network calls, filesystem operations, and SDK invocations do not move onto a domain value. Pulling an atomic update into a load-modify-store entity method can introduce a race and an extra round trip.

Do not create a repository interface, domain service, aggregate root, or event framework merely to make a model look object-oriented. Richness means one semantic owner, not more layers.

## Decide between a method and a free function

Make an unexported function a method when the behavior or policy conceptually belongs to one concrete type. The function does not need to read fields; production, classification, normalization, and policy can still belong to the owner.

Keep a free function when no honest receiver exists:

- A constructor or parser creates the value it would otherwise receive.
- Pure assembly spans several types with no single owner.
- The operation belongs to a slice, a sealed interface, or a type from another package.
- A symmetric codec pair is clearer together at package scope.

Do not attach behavior to whichever type happens to call it. A wrong receiver hides ownership more effectively than a well-named free function.

## Keep one public path per capability

Find the semantic owner before editing the API. A duplicate path exists when a method and free function are synonyms, `New` coexists with a builder or functional options, an old name forwards to a new name, a root package re-exports a child capability, or streaming and aggregate calls implement separate lifecycles.

Retain one canonical path and migrate all consumers to it. Do not leave a deprecated forwarder, alias, compatibility field, or second constructor.

Keep useful higher-level facades when they compose the canonical atomic APIs. Trace through the facade to verify that it reuses their contracts, domain types, validation rules, and lifecycle ownership, with any additional orchestration policy explicit. Remove parallel implementations rather than deleting a facade merely because callers can assemble its operation themselves. Shortening an import path is not sufficient.

Use one extension mechanism for one abstraction level. Prefer a homogeneous middleware shape or narrow structural interface over named hooks for each variation.

When both streaming and aggregate calls are justified, make one consume the other. Parsing, cancellation, partial results, backpressure, and errors must have one implementation.

## Shape interfaces at consumption boundaries

Define an interface in the consuming package and include only the methods that consumer invokes. Do not accept a whole `*Engine`, `*Client`, or `*Store` to use one or two operations.

Use a concrete type inside one cohesive subsystem when there is one implementation and no substitution, test, or module boundary. Extracting an internal interface for tidiness adds indirection without decoupling anything.

Split a fat interface when several consumers use distinct method subsets. The composition root may accept or form a union; each collaborator should still depend on its own slice.

Require behavioral conformance. Add a compile-time assertion for method shape and a reusable suite for semantics, including parameters, cancellation, errors, streams, and lifecycle behavior.

## Use one construction model

Use an explicit `Config` struct when construction has related settings. Required collaborators are validated during construction; optional fields have useful zero meanings.

Do not add functional options, a builder chain, positional optional parameters, or a second constructor beside the `Config` path. Provider adapters may use their SDK's options internally, but Scope does not expose that mechanism as its cross-provider model.

Return a concrete value or pointer according to its semantics. Do not return an interface implemented by one hidden type merely to reduce visible surface.

## Choose values, pointers, and nil deliberately

Use a value for a small, required, read-only object. Use a pointer when identity, mutation, size, or meaningful optionality requires one. Values and pointers may coexist in one signature when those semantics differ.

Do not turn an immutable constructor from a value return into a pointer for visual uniformity. Do not store derived state that a method can calculate unless measurement proves the cache matters and the invalidation model is explicit.

A nil receiver is valid only when nil has a documented domain meaning. A read accessor may return a zero value or sentinel for that state. Mutators, runtime behavior, and internal helpers should expose a construction bug rather than silently no-op.

An interface can hold a typed nil. Call `lo.IsNil` directly at boundaries where that state is possible. Do not wrap it in `isNilX`, copy its reflection logic, or spread both `value == nil` and reflection checks across callers.

A constructor returns nil on error rather than a half-built pointer plus an error.

## Keep errors useful and typed

An error message must make sense without the surrounding log. Include the failing operation, the relevant identity or boundary, and the cause.

Use these forms consistently:

- Use `errors.New` for a constant message.
- Use `fmt.Errorf` for formatting and wrap a cause with `%w`.
- Keep error text lowercase and omit a trailing period.
- Use a sentinel or typed error for stable classification; never match message text.
- Preserve the original cause through every adapter.
- Return protocol and tool outcomes in their contract-defined result shape rather than converting every failure into a Go error.
- Never discard an error unless a documented boundary deliberately classifies cancellation, aggregates failures, or defines best-effort cleanup.

Do not hand-write `fmt.Errorf("... is nil")`. Use a named sentinel when callers need classification, or `errors.New` when they do not.

## Replace magic with owned vocabulary

Name every finite protocol value, version, default, timeout, state, error class, and observation key. A value needs a name because it carries policy, even if it appears once.

Use a named string value type when the vocabulary needs validation, parsing, string conversion, or codecs. Keep numeric forms for counts, sequence numbers, bit masks, and internal state discriminants where numbers are the actual domain.

Permit `map[string]any` only at a genuinely open JSON, YAML, metadata, or SDK boundary. Convert it to a typed value before domain logic, internal configuration, or cross-package calls.

Replace hidden policy as well as literals. Turn ambient state, globals, registration order, call-stack inspection, and implicit ancestor lookup into explicit dependencies or parameters.

## Extract, inline, or delete only with evidence

Do not measure quality by function or line count. Change a helper only when a concrete redundancy signal exists.

| Signal | Action |
|---|---|
| No callers | Delete the function and its obsolete tests |
| One caller, trivial body, and a parameter that is always the same constant | Inline it and remove the redundant parameter |
| A generic helper whose type parameter is exactly its receiver's type parameter | Make it a method on that generic type |
| An implementation detail serving one type at package scope | Move it to that owner as a method or local constant |
| The same truth appears three or more times and must evolve together | Extract one named owner |

Leave these forms alone unless another problem exists:

| Case | Why it stays |
|---|---|
| Two or more callers share a cohesive helper | Inlining recreates duplication |
| A generic helper is instantiated with several unrelated types | No receiver can own all uses |
| A constructor, parser, or decoder creates a value from nothing | It has no honest receiver |
| A named predicate, codec pair, or algorithm explains intent | The name is part of readability |
| A helper lives only in `_test.go` | It does not pollute production package scope |
| Similar code changes for different reasons | Extraction would create false DRY |

Stable semantic constants are an exception to the repetition threshold. A version, timeout, default, or protocol token needs a named owner even when it appears once.

## Flatten control flow without hiding structure

Use guard clauses to reject invalid or terminal cases early. Merge nested conditions that express one predicate, and extract a cohesive branch when naming it improves the caller.

Do not treat structural nesting as cyclomatic complexity. An iterator may require nested closures and a yield check; a parser may require a recursive grammar. Flattening those shapes can make ownership and control flow less clear.

Replace a long conditional dispatch with a table only when the cases are data and share one behavior. Use polymorphism when each case owns distinct behavior. Keep an explicit switch when the finite cases form a readable closed vocabulary.

Inspect cleverness signals before preserving them: generics nested more than two levels, reflection inside domain code, `any` beyond an open boundary, long type switches, and deeply nested closures. Each may be valid, but each must remove more complexity than it introduces.

## Organize for locality and ownership

Keep behavior near the state and contract it explains. Shared code moves only to the lowest package where every consumer needs it.

Split a large file when it mixes responsibilities, ownership, or lifecycles. Keep a large cohesive parser, generated mapping, protocol family, or tightly coupled algorithm together. A line threshold triggers inspection; it does not command a split.

When a large cohesive type has irreducible field groups, a short field-zone comment may state the ownership or lifecycle distinction. Prefer named subtypes when they clarify the model; do not split state only to reduce a line count.

Prefer several responsibility-named files in one package before creating another package. Create a package when it owns a coherent public or internal abstraction and the import direction remains valid.

Create a module only for an independent dependency set, release cadence, or version boundary. Do not split modules for symmetry or merge unrelated provider SDKs into one aggregate module.

## Apply modern Go with purpose

Use the Go version declared by `go.work`, and keep every module's `go.mod` directive synchronized.

Prefer current standard-library forms when they clarify the code: `any`, `min` and `max`, `slices`, `maps`, `iter.Seq2`, integer and slice range forms, `time.Since`, `omitzero`, typed atomics, `sync.WaitGroup.Go`, and `testing/synctest`.

Use `errors.AsType` only when the target is an error type. Use an iterator instead of a channel when the caller owns pull-based consumption and no independent producer lifetime is required.

Use a nil slice for a local result accumulator unless the owning contract requires a non-nil empty collection. Preserve established non-nil fields when callers rely on their serialization or mutation behavior.

Do not modernize working code merely to change syntax. Apply a newer form when it removes manual loops, hidden ownership, races, or edge cases.

## Replace hand-written infrastructure completely

Use the standard library first. Before adding a dependency, inspect existing dependencies and verify the candidate's maintenance, boundary behavior, platform support, and dependency cost.

Adopt a third-party library when it removes parsing, edge cases, and maintenance surface on net. Core may use a dependency under the same rule; being foundational is not a reason to reimplement general capability.

After replacement, delete the local parser, formatter, codec, wrapper, and duplicate tests. Do not hide the previous implementation behind an adapter or wrap a mature function only for visual consistency.

A third-party type may cross Scope's public API only when it is the de facto type of the external protocol. Otherwise convert once at the adapter boundary and keep Scope's domain model independent.

Keep an adapter only when it owns Scope policy, error translation, authority, lifecycle, or a real protocol boundary.

## Preserve concurrency and lifecycle ownership

Every goroutine has one owner, a visible stop condition, and one component responsible for joining or detaching it. Every channel has one closer. Every transaction has one commit or rollback owner.

Propagate `context.Context` through call boundaries. Do not replace a caller context with `context.Background()`. A deliberately detached operation keeps trace values with `context.WithoutCancel` and documents who ends it.

State lock order, publication rules, callback phase, and shutdown ordering when violating them can pass compilation and fail in production. Prefer a type or state machine that makes invalid transitions impossible over comments asking callers to remember an order.

Run race tests after changing shared state, cancellation, stream ownership, callbacks, caches, or goroutine lifetime.

## Preserve the observability boundary

Core and capability modules do not import OpenTelemetry or invent tracer, meter, or logger interfaces. Instrumentation belongs in protocol integrations or decorators in the `otel` module.

Do not add incidental `slog` calls to domain code as a substitute for an owned signal. At an instrumentation boundary, record stable low-cardinality attributes, status, duration, and errors; never record prompts, credentials, documents, media, or raw provider messages.

Prefer OpenTelemetry semantic conventions. Custom attribute keys carry no Scope brand. The Host binds exporters, the `slog` bridge, and World Wide Web Consortium (W3C) propagation once at the composition root.

## Write comments only for information code cannot express

First try naming, a richer type, a smaller function, or a clearer state transition. Add a comment only when code cannot carry the constraint itself.

Valid comments explain one of these reasons:

1. A public contract: invariants, ownership, lifetime, error semantics, side effects, or concurrency guarantees.
2. An external constraint: a protocol rule, compatibility fact, provider SDK behavior, or business convention.
3. A non-obvious algorithm: its idea, edge cases, complexity, or measured optimization.
4. A counter-intuitive choice: why the more familiar implementation is wrong here.
5. A safety rule: goroutine ownership, lock ordering, transaction boundaries, cancellation, trust, or cleanup.

Do not restate a symbol name or line of code. Do not write migration history, reviewer notes, or comments such as "fixed here". Update or delete the comment in the same change as the code it constrains.

An ordinary constructor, `Validate`, or `MarshalJSON` does not need template prose that repeats its name. If a lint rule demands noise, adjust the rule or use a package-level exemption rather than manufacturing a comment that will rot.

## Test contracts, not implementation trivia

Write tests against observable semantics and architecture boundaries. A test that copies an implementation table or computes its expected value with the production helper proves only that the code agrees with itself.

Use exact expectations for discrete mappings, state transitions, wire values, error classifications, and lifecycle outcomes. Non-empty, unique, monotonic, or count-only proxies are insufficient when the contract specifies exact values.

For an important regression guard, break the protected behavior locally and confirm that the test fails for the intended reason. Restore the behavior before committing.

Add caller-visible coverage for an exported contract. Put reusable conformance suites beside the contract and run every implementation through the same suite.

Use deterministic synchronization instead of sleeps and scheduling assumptions. Prefer `testing/synctest`, explicit barriers, controlled clocks, or owner-visible state. Run race tests for concurrency behavior.

## Keep documentation executable and current

Every workspace module keeps `doc.go` as its single documentation entry. Ownership, boundaries, and public API usage belong in GoDoc and checked examples so Markdown cannot become a second, stale API. Repository-wide principles remain in the root design documents.

Ask before adding another document. Do not create permanent point-in-time audits, review diaries, or coverage reports; turn a finding into code, an executable gate, or a current ruling in GoDoc or the root design documents.

Update links, examples, package names, error names, and code-shaped Markdown in the same batch as a rename. Documentation that teaches a retired path is a second API.

Architecture gates should derive inventories from the repository rather than maintain a list that can silently become stale. A guard must fail when a new unclassified module, provider, package, or vocabulary item appears.

## Optimize only after measurement

Before a performance refactor:

1. Find the existing benchmark, profile, trace, or metric for the suspected path. Add measurement first if none exists.
2. Prove that one part dominates the workload.
3. Record realistic input sizes and confirm whether `n` is large enough for algorithmic complexity to matter.
4. Check whether a better representation or ownership model removes the cost before changing the algorithm.
5. Make the smallest change that addresses the measured bottleneck.
6. Measure again and preserve a benchmark when regression risk is meaningful.

Do not add caching, parallelism, custom allocation, a tree, a trie, or a lock-free structure from intuition. Count invalidation, memory, synchronization, and failure modes as part of the cost.

## Batch and verify the work

Keep one ownership boundary or independently revertible purpose per commit. Separate a discovered behavioral bug from an unrelated structural cleanup unless the structure is the bug's root cause.

For each batch:

1. Apply the source-level correction.
2. Migrate every affected workspace consumer and example.
3. Remove the obsolete path and stale comments.
4. Update tests, documentation, and architecture gates.
5. Run focused tests while editing.
6. Run the affected modules' build, vet, test, race, tidy, isolation, architecture, and lint checks before committing.
7. State why the change exists and why inspected false positives were left unchanged.

Run the full workspace gate before handing off a completed repository-wide round:

```sh
scripts/check.sh build vet test race tidy isolate lint
```

Push after committing unless the user asks to keep the work local.

## Refactoring checklist

Before completing a batch, verify:

- [ ] The root cause is gone rather than hidden at a symptom site.
- [ ] Names, serialization, errors, tests, and documentation use one vocabulary.
- [ ] Domain rules live on their owner, and data transfer objects remain data.
- [ ] The capability has one public path, one construction model, and one implementation of streaming or aggregate semantics.
- [ ] Interfaces are consumer-owned and no wider than their caller needs.
- [ ] Pointer, nil, error, and zero-value choices have semantic reasons.
- [ ] Stable values and dynamic data have explicit owners and boundaries.
- [ ] Helpers were extracted, inlined, or deleted only with a concrete signal.
- [ ] Packages and files reflect responsibility rather than visual symmetry.
- [ ] Concurrency, cancellation, cleanup, and observation still have one owner.
- [ ] Tests assert exact contracts and fail when the protected behavior is removed.
- [ ] `doc.go`, examples, root documentation, and architecture gates match the code.
- [ ] Focused and workspace verification are green for the batch's risk.
