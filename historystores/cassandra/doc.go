// Package cassandra persists history through a caller-owned native gocql
// session. The host owns keyspace replication and the session lifetime.
//
// Each conversation is one partition. A permanent gate and immutable append
// rows share the same table, allowing a conditional batch to advance the gate
// sequence and publish one canonical message batch with its append receipt.
// SERIAL Paxos reads and QUORUM commits override session defaults; a timed-out
// call is retried with the same history.Batch, never a newly generated ID.
//
// Clear closes the gate before removing append rows. The gate remains forever,
// including when no message was previously written. Cleanup failure leaves the
// conversation closed and another Clear retries cleanup. Every write explicitly
// disables TTL so a table default cannot expire append identity or closure.
//
// InitializeSchema creates the current table in an existing keyspace. This
// schema replaces the former per-message TIMEUUID layout without dual reads.
package cassandra
