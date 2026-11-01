// Package postgres commits run trees to PostgreSQL. Provision SchemaSQL in a
// dedicated schema before connecting; New verifies the tables without changing
// them. The pool is borrowed. Store.Close ends the store's I/O lifetime, not
// the pool or the durable history.
//
// The head, commit identity and optional Host mutation share a transaction.
// A scoped start receipt survives head advancement. Reloading either never
// takes over a writer. A lost database acknowledgment requires reconciliation;
// it cannot establish the outcome of an external tool operation.
//
// # Crash and transaction conformance
//
// The integration suite runs the public commit contract against this adapter.
// Each test owns an isolated schema; independent processes crash before or
// after durable commits and remote dispatch. Use scripts/check-tree-committer.sh
// to provision PostgreSQL; tests require SCOPE_TREECOMMITTER_POSTGRES_DSN when
// invoked directly. This verifies process failure, not power loss.
package postgres
