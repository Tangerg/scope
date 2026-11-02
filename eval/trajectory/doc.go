// Package trajectory evaluates an owned copy of a committed agent log.
//
// The append log is the only portable runtime evidence. Calls, terminal output,
// usage, elapsed time, and behavior comparisons are derived projections. A host
// classifies dispatcher intents with Coverage before model or tool payloads are
// interpreted; arbitrary dispatcher payloads remain opaque. Token accounting is
// never inferred from a successful model response without explicit provider usage.
//
// Capture reads through the head observed at entry. It is repeatable and can
// export an unresolved tree; callers Await or Join before Capture when their
// evaluation requires terminal evidence. Stable log positions retain history
// through recovery, without callback queues or a second recording lifecycle.
//
// BehaviorDigest requires a host output projection. It excludes timestamps,
// provider identifiers, writer epochs, sibling publication order, and token use.
// Raw logs may contain prompts, answers, and tool data; protect exported evidence
// under the same policy as the persisted agent log.
package trajectory
