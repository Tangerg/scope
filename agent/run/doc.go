// Package run executes deterministic Programs against an append-only tree log.
// Store.Append acknowledges every Effect intent before dispatch and every
// settlement before another Step can consume it. Checkpoints only accelerate
// replay; they cannot introduce progress. Recovery checks replayed Transitions
// against the committed log and rejects nondeterministic code.
//
// A Runtime owns the host Build identity. Its Compiler capability lets strategy
// packages freeze declarations into Program references without exposing the
// Runtime's execution authority. Bind is the atomic construction API for custom
// Definitions. Revision identifies opaque callbacks and clients; changing Build,
// Revision, a declaration, or a child reference changes the Program reference.
//
// Start is durable and detached from the request context after admission. Reuse
// the same StartID to retry submission; a conflicting declaration is rejected.
// Recover explicitly acquires a new writer Epoch. A duplicate Start never takes
// over another writer. Run instead attaches the foreground operation to its
// context. Close cancels and joins local work without declaring an uncertain
// external operation stopped; unresolved Effects remain in the durable log.
//
// Process paths own ancestry. A child key cannot be reused within its parent's
// lifetime because that would also reuse Effect identities. The tree retains a
// compact index of used paths; completed child execution state is discarded once
// its Exit is consumed and Reaped is appended. Signal receipts likewise remain
// necessary for exact retry deduplication. Neither index is a second progress
// authority: both are rebuilt from records.
//
// Completion carries a definite Effect outcome. An Unknown outcome stays pending
// until explicit resolution or a permitted replay under the same EffectID. Exit
// and Result are published only after all descendants and uncertain operations
// have drained, including after cancellation or failure. Yield declares local
// Wait values directly; the inbox never contains a wait-opening handshake.
//
// Observe reads durable records after an LSN and remains valid across recovery.
// Load retains the full log so earlier observation cursors remain usable; recovery
// restores opaque ExecutionState only from the newest Checkpoint and replays its
// suffix. Hosts own log retention. Config.Deltas is the separate best-effort
// stream: channel saturation may drop increments but cannot change committed
// usage or outcomes. Keep the channel open until Close returns.
package run
