// Command reconciliation investigates a Tool response lost after a remote
// operation, restores the acknowledged tree in a replacement Engine, and either
// settles a confirmed result or stops with the Unknown intact.
//
// Run from the repository root:
//
//	go run ./examples/agent/reconciliation
//
// Run against the versions declared by examples/go.mod, without workspace
// sources or replace directives:
//
//	cd examples
//	GOWORK=off go run ./agent/reconciliation
//
// The deterministic model, remote ledger, and MemoryTreeCommitter keep this
// example offline. They demonstrate public API consumption, not durable storage
// or live-model quality. The same source is tested with workspace dependencies
// and with the published Agent and Core versions selected by examples/go.mod.
//
// The remote ledger owns the business outcome; the TreeCommitter owns the
// acknowledged execution head. An Event only wakes the Host. Before recovery,
// the Host pauses the live parent, reads the stored head, and retains its
// original EffectRequest. After a crashed writer there is no live parent to
// pause; recovery reads the store directly. A stale head must fail activation.
// The Host restores the exact Deployment, queries the remote operation by
// EffectID, and checks the receipt against the original arguments. Only a confirmed success
// or rejection becomes a ToolSet.SettleToolResult submitted through
// Process.ResolveUnknownEffect. No snapshot or Strategy state is edited.
//
// A missing receipt or failed query does not establish failure or permission to
// repeat the write. The example cancels the tree and retains its unresolved
// identity. Cancellation is terminal: later receipt discovery cannot resume
// this execution. Further business reconciliation remains a Host concern.
// Interaction forbids Tool replay, including after restoration. A
// completed Interaction may report a confirmed business rejection; its terminal
// status alone does not establish business success.
package main
