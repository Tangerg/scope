// Command reconciliation investigates a Tool response lost after a remote
// operation. It restores the committed log in a replacement Runtime and settles
// only a receipt that matches the original EffectID and arguments.
//
// Run from the repository root:
//
//	go run ./examples/agent/reconciliation
//
// The deterministic model, remote ledger and MemoryStore keep the example
// offline. The ledger owns the business outcome; the Store owns execution facts.
// Handle.Observe reads the committed Unknown before the original runtime closes.
// Recover loads the same root and activates a new writer epoch. The exact compiled
// program keeps the ordinary tool call within its interaction process.
//
// A missing receipt or failed query establishes neither failure nor permission
// to repeat the write. Cancellation requests a stop, but the tree cannot publish
// Exited while its external outcome remains Unknown. Runtime.Close joins local
// work and retains that unresolved fact for a later reconciliation. A confirmed
// receipt becomes interaction.SettleToolResult followed by ResolveUnknownEffect.
// A completed interaction can report a confirmed business rejection, so its
// terminal status alone does not establish business success.
package main
