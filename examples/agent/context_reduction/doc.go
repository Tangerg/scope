// Command context_reduction compares one deterministic message reduction with
// the unchanged context on 64-round report-reading tasks. It reuses Interaction's
// ModelContextReducer and committed WorkingContext; no history store or replay
// layer participates in reduction. Run go run ./examples/agent/context_reduction
// from the workspace root, or GOWORK=off go run ./agent/context_reduction from
// examples to use the published dependencies selected by its go.mod.
//
// The opt-in example policy replaces byte-identical successful Tool text of at
// least 1 KiB with a short backward reference to the first full output from the
// same Tool. Call/result pairs, unique outputs, system constraints, and user
// goals remain intact. Errors, structured output, media, annotations, ambiguous
// identities, and references too large to remain below 1 KiB are left unchanged.
// It is a task policy, not an enabled-by-default Interaction behavior.
//
// Paired eval checks require zero exact-answer degradation and at least 50%
// fewer serialized model-request bytes on the fixed dataset. A recreated Engine
// restores a strict committed snapshot without replay or regrowing old output.
// The local model explicitly resolves references; this proves the deterministic
// protocol and task contracts, not live-model quality, tokenizer savings, or
// provider charges. MemoryTreeCommitter retains state only for its own lifetime.
package main
