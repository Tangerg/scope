# Scope design philosophy

This conceptual document explains why Scope is shaped the way it is. [`AGENTS.md`](AGENTS.md) contains the short repository rules, and [`REFACTORING.md`](REFACTORING.md) turns these principles into an editing and verification method. Package-specific contracts live with their GoDoc and checked examples.

## The governing idea

Scope has one organizing idea: a thin, indivisible kernel; capabilities that reduce to that kernel; and no capability that rebuilds the kernel's infrastructure.

The kernel owns only contracts that every implementation must obey. Concrete provider behavior, optional capabilities, protocol integrations, and application policy stay above it. This keeps the base abstraction broad enough to compose without making every consumer carry concepts it does not use.

## The two repair laws

Scope is still in active development, so backward compatibility with a known-wrong design is not a goal. Two laws govern design changes:

1. Do not take on debt for convenience. Once the better shape is known, replace the wrong application programming interface (API), schema, name, or dependency direction instead of keeping an alias, migration, fallback, or temporary shim.
2. Fix the root cause in the layer that owns it. A consumer-side condition, retry, coercion, or log entry that only hides an upstream invalid state is not a fix.

Scope-owned state and persistence have one current schema during development. Validate that schema directly. Do not introduce schema-version envelopes, version dispatch, migration registries, or dual reads/writes to preserve a superseded shape. A module release number does not become a domain state field.

A repair is complete only when the cause no longer exists. If the original invalid state can still be produced and one symptom merely stopped appearing, the change is a patch.

Breaking exported APIs, wire shapes, and schemas still require an explicit blast-radius discussion before editing. Once approved, replace the old design outright and migrate every workspace consumer in the same change.

## Explainable and explicit design

The useful parts of the Zen of Python apply directly to framework design:

- Prefer explicit relationships over implicit behavior. Dependencies, ownership, policy, identity, and lifecycle choices appear in types, declarations, or parameters.
- Prefer a direct model over a complicated one. Necessary complexity remains visible; indirection does not pretend the complexity disappeared.
- Prefer flat and sparse structure until nesting represents real ownership, composition, or protocol shape.
- Treat readability as a correctness property. If the public model cannot explain the implementation, first assume the model or implementation is wrong.
- Do not let special cases create a second semantic path. A genuine semantic difference gets a separate owner; an incidental difference uses the existing abstraction.
- Let practical evidence correct theory, but do not weaken invariants to accommodate one integration.
- Reject ambiguous input, ownership, or capability selection instead of guessing.
- Keep one obvious API for one meaning. Discoverability comes from naming, documentation, and examples rather than duplicate entry points.
- Implement a proven need now as a complete vertical slice. Wait when the design is not understood; incomplete urgency is worse than deliberate omission.
- Treat an implementation that is hard to explain as a design warning, not as a reason to write a longer comment.
- Use namespaces and package boundaries to communicate ownership and prevent collisions.

## Scope's framework boundary

Scope is reusable Go infrastructure for models, agents, retrieval-augmented generation (RAG), data processing, tools, evaluation, and protocol integration. It is not an application platform.

| Boundary | Responsibility |
|---|---|
| Repository root | A Go workspace and release coordination point; never a root Go module or public facade |
| `core` | Provider-neutral protocols, minimal service provider interfaces, direct clients, shared values, and reference implementations |
| `agent`, `rag`, `etl`, `eval`, `tools`, `skills` | Reusable capability modules built on lower contracts |
| `a2a`, `mcp`, `otel` | Protocol and observability integrations that adapt contracts from the outside |
| Provider modules | Independently versioned leaf adapters with provider-specific dependencies and release cadence |
| Flame | Product sessions, desktop workflows, deployment catalogs, dashboards, billing, marketplaces, and other application concerns |

A module expresses an independent dependency set, release cadence, and version boundary. A package expresses one responsibility inside that boundary. Splitting modules for visual symmetry creates release overhead without isolation; merging unrelated provider dependencies couples versions that should move independently.

`models/<provider>`, `vectorstores/<provider>`, and `historystores/<provider>` are separate modules because their software development kits (SDKs) and release cadences differ. `tools/web/<provider>` remains a package family because those providers share one lightweight dependency set and lifecycle.

The project identity is `Scope`, and Go module paths start with `github.com/Tangerg/scope`. External provider and protocol names appear only where integration facts require them; they do not become naming anchors for Scope concepts.

## Three shapes of variation

New behavior should reuse the existing runtime before it proposes another one. Scope recognizes three shapes of variation, in this order:

| Shape | What varies | Preferred form |
|---|---|---|
| Parameterize | One policy or decision | A value or narrow strategy passed to the owner |
| Compose | Existing primitives form a higher capability | A type that owns the composition and compiles back to the lower contracts |
| Decorate | An existing call gains cross-cutting behavior | Middleware or a decorator around the contract boundary |

Use this sequence when designing a capability:

1. Ask whether a value or policy parameter expresses the difference.
2. If not, ask whether existing primitives can be composed under one real lifecycle owner.
3. If not, ask whether the behavior only decorates an existing contract.
4. If none fits, stop before adding a second runtime, scheduler, registry, or state carrier. Decide whether the missing concept belongs in the kernel, in a higher capability, or in Flame.

Composition is preferred over inheritance. Go methods and interfaces express behavior without a framework base class, dependency injection container, reflection registry, or privileged hook hierarchy.

## Put abstractions at the correct layer

An abstraction's layer is decided by how widely its contract applies, not by where its name sounds natural.

A mandatory rule that every implementation must obey belongs at the lowest shared layer. An optional capability used by one consumer stays with that consumer, which defines the narrow interface it needs. Sinking consumer-specific behavior into Core makes every unrelated implementation carry a concept it cannot use.

Concreteness accumulates upward. Core remains provider-neutral; capability modules add reusable semantics; integrations add protocol boundaries; providers add SDK details; the Host selects concrete implementations and application policy.

The placement test is direct: does every consumer need this to remain correct, or does one consumer choose to use it? The first answer can justify a kernel contract. The second requires a consumer-owned abstraction above the kernel.

## Dependency direction and interface ownership

Package and module imports form a directed acyclic graph. Higher layers depend on lower contracts, provider modules depend on Core, and integrations depend on the contracts they adapt. Sibling providers do not import one another, and lower layers never import the compositions built above them.

Interfaces are defined by consumers because only a consumer knows the behavior it requires. Cross-package and public substitution boundaries use the smallest useful interface. Inside one cohesive package with one implementation and no substitution need, the concrete type is clearer than a ceremonial interface.

The composition root selects concrete implementations. It is the only place that should know both sides of several boundaries. A constructor or upper-level owner may accept a union of narrow interfaces, while each internal collaborator depends only on its own slice.

Substitutability is behavioral, not syntactic. An implementation must honor every parameter, error, cancellation, streaming, and lifecycle promise of its interface. Compile-time assertions prove method shape; conformance suites prove the shared behavior.

## Cohesion, coupling, and extension

The familiar design principles are decisions, not slogans:

| Principle | Scope interpretation |
|---|---|
| High cohesion | A package, type, or function has one coherent reason to change |
| Low coupling | A boundary exposes the least information needed by its consumer |
| Single responsibility | Split mixed ownership or mixed lifecycle, not a large cohesive algorithm |
| Open and closed | A real extension arrives as a new implementation of an existing contract, not another branch in a central dispatch loop |
| Interface segregation | Consumers depend on the methods they use; assembly may compose those interfaces |
| Dependency inversion | High-level capability code owns the abstraction and receives provider implementations |
| Don't repeat yourself (DRY) | Share one truth that must evolve together; do not couple similar code that changes for different reasons |
| Keep it understandable | Prefer ordinary control flow, named values, and local reasoning over reflection, clever generics, or hidden dispatch |
| You are not going to need it (YAGNI) | Add an extension point after the variation exists, not when it is only imaginable |

A large cohesive parser, engine, or protocol package is not automatically a god package. Split it only when the split gives each side a distinct responsibility and severs a real coupling. Function count, file count, and line count are signals for inspection, not design goals.

## One meaning, one public API

Every atomic capability has one semantic owner, one public representation, and one canonical API. A method, free function, alias, builder, compatibility wrapper, and alternate stream implementation cannot coexist as synonyms.

Higher-level convenience facades may compose canonical atomic APIs into useful operations. They reuse the same contracts, domain types, validation rules, and lifecycle ownership; any additional orchestration policy is explicit. A facade must not reimplement an atomic capability or introduce a parallel API family. Re-exporting lower symbols or shortening an import path does not create a capability.

Extension follows the same rule. Prefer one homogeneous mechanism, such as one middleware shape or one structural interface, over a named hook for each variation. Each additional mechanism creates another ordering model, error path, documentation surface, and compatibility obligation.

Streaming and aggregate forms may coexist only when one is the source of truth. The aggregate form consumes the stream completely; it does not maintain separate parsing, cancellation, error, or lifecycle behavior.

## Behavior-rich domain models

Scope prefers object-oriented, behavior-rich domain models over procedural logic operating on anemic data bags. In Go, object-oriented means encapsulated state plus methods on the type that owns the semantics, not inheritance or a class hierarchy.

Entities and value objects own their invariants, validation, derived values, and pure state transitions. Moving these rules onto the owner makes invalid states harder to construct and keeps one semantic API. A procedural service that repeatedly inspects and mutates another type's fields usually signals misplaced behavior.

Data that represents data remains data. Configuration, request and response data transfer objects, wire protocol values, plain parameters, and fact records do not need decorative methods or empty service objects.

Input/output (I/O) marks the boundary. Pure rules belong to the domain owner; network calls, transactions, filesystem operations, and atomic database updates remain in adapters. Moving an atomic write into a load-modify-store entity method would reduce correctness rather than enrich the model.

A rich model does not require a speculative domain-driven design stack. Add repositories, application services, aggregates, and domain events only when distinct implementations, lifecycles, or transactional boundaries require them.

## Stable vocabulary and no magic

Every noun names one concept and lifecycle across code, comments, errors, tests, and documentation. Two names for one meaning create two mental models; one name for two lifecycle stages hides ownership.

A stable finite vocabulary uses a named value type or constant owned by its domain. Protocol versions, defaults, timeouts, states, error classes, and observation keys need names even when each appears once. Their meaning, not repetition count, requires an owner.

Anonymous dynamic data stops at an open boundary. JSON, YAML, metadata extensions, and third-party SDKs may require `map[string]any`; internal configuration, domain state, and cross-package arguments use typed values.

Magic includes more than literals. Ambient state, call-stack inspection, ancestor lookup, registration order, implicit globals, and reflection-based discovery also hide policy. Make the dependency or decision visible in a declaration or parameter.

## Construction and protocol values

Related construction settings use an explicit `Config` value. Optional fields have useful zero meanings, and required collaborators are validated at construction. Scope does not promote provider SDK functional options into its cross-provider API and does not add builder chains beside `New`.

A capability constructor takes a `context.Context` first, uniformly across a family, whether or not it uses one today. Some backends must be reached at construction — a vector store reads the metric its scores depend on, a Cosmos container its partition-key path, a Google or Bedrock client its credentials — and a constructor without a context cannot check the configuration it was handed, which is how an unchecked obligation on the caller becomes wrong output nothing can detect. Splitting the signature by which provider happens to need it makes callers memorize the exception list, so the parameter is required everywhere and named `_` where it is unused, which says plainly that construction performs no I/O.

Accept interfaces and return concrete values. Inputs gain compatibility from narrow consumer-owned interfaces; outputs preserve information and behavior through concrete types. Return pointers only when identity, mutation, size, or meaningful optionality requires one.

Useful zero values reduce constructors and invalid transitional states. Zero-value usefulness does not excuse an ambiguous zero, an unvalidated required collaborator, or a hidden default with protocol meaning.

Protocols stay serializable and provider-neutral. Business code does not depend on SDK request types or transport envelopes. Streaming uses pull iterators when the caller owns consumption; a provider that cannot stream does not implement a fake streaming method.

## Error ownership

Errors retain their layer's semantics. A stable classification is a sentinel or typed error owned by the domain, never a message substring. Wrapping preserves the cause, and an error remains understandable without surrounding log context.

Protocol failures, tool results, business outcomes, and transport failures are not interchangeable. Each boundary decides whether a condition is a Go error, a protocol result, or a typed domain outcome, then preserves that decision through adapters.

Errors never disappear accidentally. A boundary may deliberately classify cancellation, aggregate several failures, or suppress a documented best-effort cleanup error. Silence without an explicit contract is data loss.

## Observability stays outside the domain

Observability consists of traces, metrics, and logs through the official OpenTelemetry API. Scope does not invent another tracer, meter, or logger abstraction.

Core and capability modules remain independent of OpenTelemetry. The `otel` module decorates Core contracts from the outside. Agent-to-Agent (A2A) and Model Context Protocol (MCP) integrations may instrument the protocol boundary they own; that permission does not spread into the capabilities they adapt.

The Host binds exporters and World Wide Web Consortium (W3C) propagation once at its composition root. Logs reach the OpenTelemetry LoggerProvider through the `slog` bridge. Domain code does not emit incidental logs in place of owned runtime signals; adapters record spans, metrics, status, and errors at the call boundary.

The trace identifier originates at the entry point and propagates through every boundary. Telemetry records stable identities, counts, durations, outcomes, and low-cardinality classifications. Prompts, documents, media, credentials, and raw provider messages do not become attributes. Prefer semantic conventions; custom keys carry no Scope brand. The instrumentation scope may use the library path because it identifies the instrumentation library rather than domain data.

A detached goroutine preserves trace values with `context.WithoutCancel` rather than starting from `context.Background()`.

## Performance follows evidence

Rob Pike's five rules define Scope's performance discipline:

1. Do not guess where time is spent. Bottlenecks appear in unexpected places, so profile the real workload.
2. Measure before tuning. Change code only when one part dominates the workload, then measure again and preserve a benchmark when regression risk matters.
3. Assume `n` is small until evidence says otherwise. Constants, allocation, cache locality, and maintenance cost often dominate theoretical complexity at real input sizes.
4. Prefer straightforward algorithms and data structures. Clever algorithms create more states and more bugs, so evidence must pay for their complexity.
5. Let data dominate. A representation with correct ownership and indexing usually makes the algorithm obvious.

Parallelism, caching, custom allocators, tries, trees, and lock-free structures are not improvements by themselves. Each needs a measured bottleneck and a result that remains better after its complexity, memory, and failure modes are counted.

## When principles conflict

Use these tie-breakers when two valid principles point in different directions:

- DRY versus low coupling: keep repetition when extraction would couple code that evolves independently or introduce a forbidden dependency.
- Interface segregation versus simplicity: use a concrete type inside one cohesive implementation; split interfaces at real consumer or substitution boundaries.
- Open extension versus YAGNI: retain an extension point after the variation has occurred; do not add one for a hypothetical implementation.
- Purity versus practicality: preserve semantic invariants, but choose the implementation that fits real workloads and boundaries.
- Discoverability versus one entry: improve names, package documentation, examples, and catalogs instead of creating a facade or alias.
- Uniformity versus meaning: allow different shapes when they represent different identity, optionality, lifecycle, or ownership; do not force symmetry that erases semantics.

## Directions already rejected

These designs have been considered and must not return without new evidence that changes their premise:

- A framework-wide retry layer or transient-error taxonomy; provider SDKs already own retry policy.
- A second structured-output conversion chain; the parser family is the single owner.
- A root facade that re-exports capabilities from their semantic packages.
- A fat interface or whole engine, client, or store passed across a boundary that uses only a few methods.
- Duplicate public enums, compatibility aliases, speculative hooks, placeholder interfaces, or empty service layers.
- OAuth and token refresh inside a model provider; the Host supplies and replaces credentials.
- A second observability abstraction or OpenTelemetry imports in Core and capability modules.
- An application module or Flame product concern inside Scope.
- A custom parser, codec, or infrastructure primitive retained beside a mature dependency that replaced it.

## Design review checklist

Before adding a capability, package, module, or exported contract, verify:

- [ ] The capability belongs in Scope rather than Flame.
- [ ] The design uses parameterization, composition, or decoration before introducing another runtime.
- [ ] The contract sits at the lowest layer where every consumer needs it, and no lower.
- [ ] Every dependency points downward, and each interface is owned by its consumer.
- [ ] The capability has one semantic owner, representation, entry point, and extension mechanism.
- [ ] Domain behavior lives on its owner, while wire and configuration data remain data models.
- [ ] Stable vocabulary, defaults, versions, timeouts, and errors have named owners.
- [ ] Dynamic maps and provider SDK types stop at their boundary.
- [ ] Configuration uses one explicit `Config` shape with meaningful zero behavior.
- [ ] Streaming, cancellation, errors, and lifecycle semantics have one source of truth.
- [ ] Observability remains outside Core and capability modules.
- [ ] Performance choices follow measurements from a real workload.
- [ ] A breaking change has a stated blast radius and migrates every workspace consumer without a compatibility layer.
- [ ] Code, tests, documentation, and architecture guards can describe the same model.
