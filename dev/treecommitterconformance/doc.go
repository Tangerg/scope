// Package treecommitterconformance verifies Agent's public TreeCommitter
// contract against PostgreSQL transactions and independent runtime processes.
// The PostgreSQL adapter is a private test consumer, not a supported storage API.
// Agent owns lifecycle, commit identity, and content; the adapter owns atomic
// storage acknowledgment, its business write set, I/O deadline, and shutdown.
// It retains canonical snapshot bytes and the separate commit sequence without
// introducing another writable Process status, writer identity, or head digest.
//
// Run scripts/check-tree-committer.sh from the repository root. It provisions
// one isolated PostgreSQL container and runs tagged vet, lint, and vulnerability
// checks. Integration tests run with race detection both in the workspace and
// against the module's published dependencies.
// Integration tests fail when SCOPE_TREECOMMITTER_POSTGRES_DSN is absent; they
// never skip missing infrastructure. Each test owns a fresh database schema.
// Crash tests kill runtime processes at signaled transaction and dispatch cuts,
// then read and recover through a different process and database connection.
// Host acceptance records commit with the head; external dispatch receipts are
// separate effects, whose uncertain outcomes require explicit reconciliation.
// These checks cover process failure, not power loss or faulty storage hardware.
package treecommitterconformance
