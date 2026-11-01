# Development tooling

This directory holds the modules that check the repository itself. It is a
namespace, not a module: every tool below it is an independently versioned leaf
with its own `doc.go`.

## Ownership

- `repoarch` owns repository-wide module, package-layer, naming, and provider
  boundary invariants.
- `providerconformance` owns the cross-provider consistency that no single
  provider module can check for itself.

## Dependencies

- These modules may read the whole workspace, which is why they live outside
  the product dependency graph.
- No product module depends on them, and nothing here ships.

## Invariants

- A gate states an exact expectation and fails in both directions: a new
  package cannot silently escape it, and a removed rule cannot silently pass.
- Architecture rules live here as executable tests rather than as prose that
  can drift from the code it describes.
