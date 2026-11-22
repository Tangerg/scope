# Model adapters

Scope's model adapters connect model services to Core's provider-neutral
modality contracts. This directory is a namespace, not a module: every adapter
below it is an independently versioned leaf with its own `doc.go`.

## Ownership

- Each leaf owns only its external boundary, configuration, and translation.
- Core owns portable semantics and the contract test suites.
- The family owns translation boundaries, not shared model semantics,
  orchestration, or product policy.

## Dependencies

- A leaf may depend on Core and the external service it adapts.
- Leaves do not import sibling adapters or higher-level capability modules.

## Invariants

- External details do not alter or leak through shared contracts.
- Every portable option has one Core-owned representation; adapters do not add
  provider-specific synonyms for the same capability.
- Adapters translate a portable semantic exactly or reject it before provider
  I/O; they never silently weaken an unsupported request.
- Complete provider outputs and stream events map to their corresponding Core
  values without manufacturing partial stable content.
- Construction, authority, provider-only extensions, and unsupported
  capabilities are explicit.
- One function owns each adapter's option rules. Construction applies it to
  `DefaultOptions` and every call applies it to the resolved options, so
  defaults that no request could send fail when they are configured. A value
  a request may supply instead, such as the model or a voice, is required only
  of the resolved options; Core's `Options.Resolve` requires the model in every
  modality.
- An adapter requires `DefaultOptions.Model` at construction only when the
  model selects part of the binding or of the wire contract, such as an Azure
  deployment, a Replicate schema, or a Bedrock embedding family.
- Runtime policy and product workflows remain outside adapter modules.

Each adapter's own boundary, public API, and executable usage live in its
`doc.go`, GoDoc, and checked examples.
