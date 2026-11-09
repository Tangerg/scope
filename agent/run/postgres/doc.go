// Package postgres atomically appends run records and optional Host writes in
// one PostgreSQL transaction. The append-only log is the only durable fact:
// its last entry is the head an append compares against, under a per-root
// advisory lock that serializes creation and every later append.
//
// Install SchemaSQL in a dedicated Host-owned namespace before calling New.
// The Host owns pool lifetime, authorizes namespace creation, and joins its
// runtimes before closing a Store or pool. Load finds the newest checkpoint
// through a partial index, so recovery reads never scan the history before it;
// Read streams history from one snapshot.
//
// Config.Prepare must derive its mutation solely from the exact Batch. It runs
// only for a new append, inside the root's serialized transaction. A byte-exact
// retry is acknowledged without repeating Host writes. Prepare and Apply must
// not contact remote services or commit their own transaction; those effects
// cannot participate in PostgreSQL rollback.
//
// Store satisfies run.Store and is exercised by runtest.RunStoreConformance.
// Run scripts/check-run-store.sh for the isolated database and process-crash
// checks; no credentials or shared database are used by the default tests.
package postgres
