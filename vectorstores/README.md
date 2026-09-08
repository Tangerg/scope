# Vector-store adapters

Scope's vector-store adapters connect storage services to Core's
provider-neutral vector-store contracts. This directory is a namespace, not a
module: every adapter below it is an independently versioned leaf with its own
`doc.go`.

## Ownership

- Each leaf owns only its external boundary, configuration, and translation.
- Core owns portable semantics and the contract test suites.
- The family owns backend translation, not retrieval semantics, embedding
  policy, or product search.

## Dependencies

- A leaf may depend on Core and the external service it adapts.
- Leaves do not import sibling adapters or higher-level capability modules.

## Invariants

- External details do not alter or leak through shared contracts.
- Construction, authority, and unsupported capabilities are explicit.
- Runtime policy and product workflows remain outside adapter modules.

## A successful call is not a completed operation

Storage services routinely report an incomplete operation inside a successful
response, so an adapter states what its evidence actually proves. Three
questions decide it, and each adapter answers them in its own `doc.go`:

- **Did every item apply?** A batch endpoint that answers 200 with per-item
  results has not acknowledged anything until those results are read. Require
  one successful result per item sent, and treat a missing, extra, or repeated
  result as an unaccounted item rather than a success.
- **Is this result complete?** Timeouts, lost shards, and degraded coverage
  return partial data with a successful status. A page shorter than the
  requested limit, or an empty page, proves exhaustion only when the service
  says so — read the continuation token or the reported totals instead of
  inferring the end from a page's length.
- **Does the primitive answer the question asked?** An approximate
  nearest-neighbor query returns up to *k* candidates, never every match, so it
  cannot enumerate a filter. When a service offers no exhaustive filtered
  operation, enumerate with a listing primitive and decide membership with
  [`filter.Match`](../core/vectorstore/filter/match.go) rather than
  reimplementing the predicate semantics.

Where a service genuinely cannot offer atomicity, say what state a failure
leaves behind instead of implying a rollback. Adapters keep their provider
dependency narrow — an interface naming the operations they call — so this
accounting is checkable without a live backend.

Each adapter's own boundary, public API, and executable usage live in its
`doc.go`, GoDoc, and checked examples.
