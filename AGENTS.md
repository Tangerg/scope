# Repository instructions

Scope is a Go workspace of independently versioned AI infrastructure modules. It is a framework and library, not an application platform. Flame owns product sessions, desktop workflows, dashboards, marketplaces, billing, and deployment catalogs. The repository root stays a workspace without a Go module, and every module path starts with `github.com/Tangerg/scope`.

Package contracts live in GoDoc and checked examples. Read [`DESIGN_PHILOSOPHY.md`](DESIGN_PHILOSOPHY.md) before designing a capability and [`REFACTORING.md`](REFACTORING.md) before refactoring.

For ongoing audits and refinement, follow [`refactor-prompt.md`](refactor-prompt.md) to establish evidence, complete bounded repair batches, and stop when no justified work remains.

- Do not preserve backward compatibility. Fix a wrong design at its owning layer, then remove obsolete APIs, schemas, aliases, fallbacks, migrations, and shims.
- Prefer explicit, readable, flat, sparse code. Implement a proven need as the smallest complete end-to-end slice, keep necessary complexity visible, and reject speculative or hard-to-explain indirection.
- Treat repository-local usage as no evidence for or against a public API. Give each capability one owner, one public representation, and one obvious call path; do not add synonymous methods, functions, builders, aliases, wrappers, or root facades.
- Make every domain noun name one concept and lifecycle across code, comments, errors, and documentation. Prefer object-oriented, behavior-rich domain models over procedural logic around anemic data bags: put invariants, validation, derived values, and pure state transitions on the domain owner, while configs, wire values, requests, responses, and facts remain data models.
- Make dependencies, policy, vocabulary, and errors explicit. Do not use magic values, anonymous maps, ambient state, registration order, or hidden globals as domain state. Resolve ambiguity instead of guessing, and never discard an error unless the contract explicitly permits it.
- Keep dependencies one-way. Define narrow interfaces in the consuming package, depend on only the methods it uses, and do not pass a whole engine, client, or store across a smaller boundary.
- Use the standard library first, then existing mature dependencies when they reduce total complexity. Check their documentation and types before wrapping or reimplementing them. Use explicit `Config` structs, not functional options or builders, for Scope construction settings.
- Use the Go version in `go.work` and its current standard library. Receivers use their type's lowercase initial, parameters never shadow imported packages, and typed-nil checks call `lo.IsNil` directly.
- Do not guess where performance matters. Measure first, optimize only a dominant bottleneck, and measure again. Assume `n` is small until data proves otherwise, prefer straightforward algorithms and structures, and fix the data model before adding clever code.
- Do not reintroduce a framework-wide retry layer, transient-error taxonomy, second structured-output conversion chain, fat interface, duplicate public type, speculative service provider interface, or provider-owned OAuth refresh.
- Keep OpenTelemetry outside Core and capability modules. Integrations decorate protocol boundaries from the outside; the repository design documents own the cross-module observability rules.
- Tests protect observable contracts and architecture boundaries with exact expectations. Every module keeps `doc.go` as its sole module entry; public usage lives in GoDoc and checked examples, not parallel module Markdown. Update code, tests, documentation, and architecture guards together, and do not preserve point-in-time audits as permanent documentation.
- Preserve unrelated user changes. Discuss a breaking exported API, wire, or schema change before applying it, then replace the old design without a compatibility layer. Ask before adding a document.
- Reply to the user in Chinese. Keep code, identifiers, comments, errors, and repository documentation in English. Comments explain why, not what.
- Before committing, run build, vet, test, race, tidy, isolation, architecture, and lint checks for the affected workspace. Keep commits independently revertible and push unless the user asks to stay local.
