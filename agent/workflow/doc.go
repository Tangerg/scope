// Package workflow composes deterministic orchestration from Call, Transform,
// Switch, Fork, Map, and Loop stages. [Compile] freezes the graph and its child
// Programs using the Runtime's Compiler. A callback Revision identifies opaque
// code; Configuration records settings captured by that code. Graph bindings,
// schemas, budgets, and bounds have their sole owner in the stage declarations.
//
// Child invocations advance from a Spawn Completion to an Exit. A successful
// Spawn admits work; only Exit establishes that its subtree has drained. The
// Runtime owns cancellation, mailbox delivery, persistence, and reaping. Workflow
// owns only its stage cursor, business values, and outstanding invocations.
//
// Fork and Map preserve declaration order in their output while admitting
// bounded windows of child work. A failed admission or child execution preserves
// its original Failure. A window drains before the workflow adopts its results,
// propagates a failure, or starts another window.
//
// Transform, Switch, Fork, and Loop callbacks run inside discardable Steps.
// They must be deterministic, bounded, side-effect-free, and cancellation-aware.
// Context carries cancellation, never hidden domain input. External I/O belongs
// in a child Program's dispatcher Effects.
//
// Persisted execution state contains only progress not already owned by the
// immutable graph. Restore validates it against the exact compiled graph.
// Unaddressed Signals are rejected; Completion and Exit messages must match
// outstanding invocation identities.
package workflow
