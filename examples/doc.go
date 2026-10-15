// Package examples contains runnable demonstrations, with no supported API.
// Examples may import Scope modules; Scope modules must not import examples.
//
// Command chat uses only Core for an ordinary structured call. Command
// agent/direct_vs_managed contrasts ordinary calls with an Interaction under
// an Engine, whose explicit MemoryTreeCommitter lasts only for that instance.
// Both are offline consumers; Agent is absent from chat's dependency closure.
//
// Command agent/reconciliation restores an Interaction with an uncertain remote
// write and investigates its receipt through the public settlement APIs. It
// includes confirmed success, confirmed rejection, missing receipts, and query
// failure. With GOWORK=off, it consumes the published versions in this module's
// go.mod, without local replacements or a separate consumer manifest.
// Run MODULE=examples scripts/check.sh isolate from the repository root for
// module hygiene and every consumer's behavior against those dependencies.
// Command agent/context_reduction compares a deterministic duplicate-output
// policy with unchanged context using paired eval/trajectory task evidence.
// Its fixed quality and context-size gates include restoration without replay.
// The Go requirement lives in go.mod; the workspace selects its toolchain in
// go.work. For PostgreSQL transactions and independent-process crash recovery,
// run scripts/check-tree-committer.sh; its module overview describes the scope
// and isolated Docker environment. These examples do not replace that proof.
package examples
